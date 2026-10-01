package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// =============================================================================
// MongoDBDriver
// =============================================================================

// MongoDBDriver implements DBDriver for MongoDB databases.
// Direct mode uses the mongo-driver/v2 Go driver.
// SSH-tunnel mode uses mongodump / mongorestore CLI tools with --archive.
type MongoDBDriver struct{}

// NewMongoDBDriver returns a new MongoDBDriver.
func NewMongoDBDriver() *MongoDBDriver { return &MongoDBDriver{} }

var _ base.DBDriver = (*MongoDBDriver)(nil)

// mongoAdminDB is the database MongoDB always provides. Server-level calls enter
// through it — it is where credentials authenticate by default, and where the
// listDatabases command is issued.
const mongoAdminDB = "admin"

// =============================================================================
// Dump File Format (direct mode)
// =============================================================================

// mongoDumpFile is the JSON container written by dumpViaDriver and consumed by
// restoreViaDriver. SSH-tunnel mode uses mongodump --archive instead.
type mongoDumpFile struct {
	DBMSType    string                       `json:"dbmsType"`
	Database    string                       `json:"database"`
	Collections []mongoCollDump              `json:"collections,omitempty"`
	Views       []mongoViewDump              `json:"views,omitempty"`
	Indexes     map[string][]json.RawMessage `json:"indexes,omitempty"`
	Data        map[string][]json.RawMessage `json:"data,omitempty"`
}

// mongoCollDump holds create options and validator for a single collection.
type mongoCollDump struct {
	Name      string          `json:"name"`
	Options   json.RawMessage `json:"options,omitempty"`
	Validator json.RawMessage `json:"validator,omitempty"`
}

// mongoViewDump holds view definition extracted from listCollections.
type mongoViewDump struct {
	Name     string          `json:"name"`
	ViewOn   string          `json:"viewOn"`
	Pipeline json.RawMessage `json:"pipeline"`
}

// =============================================================================
// Dump
// =============================================================================

// Dump exports the MongoDB database at loc to outputPath according to scope.
// Direct mode writes a structured JSON file.
// SSH-tunnel mode pipes mongodump --archive output into outputPath.
// The dump option is ignored: its only field renames PostgreSQL schemas, and
// MongoDB has no schema layer between a database and its collections.
func (d *MongoDBDriver) Dump(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer, _ *base.DumpOption) error {
	if scope == "" {
		scope = base.ScopeFull
	}
	logger.Info("mongodb dump started", "database", loc.Database, "scope", scope, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.dumpViaDriver(ctx, loc, scope, outputPath, w)
	}
	return d.dumpViaSSH(ctx, loc, scope, outputPath, w)
}

func (d *MongoDBDriver) dumpViaDriver(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer) error {
	client, err := d.openClient(loc)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)

	db := client.Database(loc.Database)
	dump := mongoDumpFile{
		DBMSType: base.DBMSTypeMongoDB,
		Database: loc.Database,
	}

	if scope == base.ScopeSchemaOnly || scope == base.ScopeFull {
		if err := d.dumpSchemaViaDriver(ctx, db, loc.Filter, &dump); err != nil {
			return fmt.Errorf("dump schema: %w", err)
		}
	}
	if scope == base.ScopeDataOnly || scope == base.ScopeFull {
		if err := d.dumpDataViaDriver(ctx, db, loc.Filter, &dump); err != nil {
			return fmt.Errorf("dump data: %w", err)
		}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}

	enc := json.NewEncoder(dst)
	enc.SetIndent("", "  ")
	return enc.Encode(dump)
}

// dumpSchemaViaDriver extracts collection options, validators, views, and indexes.
// Extraction order: Collections(options) → Validators(embedded in options) →
// Views → Indexes.
func (d *MongoDBDriver) dumpSchemaViaDriver(ctx context.Context, db *mongo.Database, filter *filterpkg.DBMSFilterOption, dump *mongoDumpFile) error {
	cursor, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		return base.Obj{Kind: base.ObjectKindCollection, Owner: db.Name()}.Fail(base.ActionList, err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		raw := cursor.Current
		name, _ := raw.Lookup("name").StringValueOK()
		collType, _ := raw.Lookup("type").StringValueOK()

		if !mongoIncludeCollection(name, filter) {
			continue
		}

		optsVal := raw.Lookup("options")
		var opts bson.Raw
		if optsVal.Type == bson.TypeEmbeddedDocument {
			opts = optsVal.Document()
		}

		if collType == "view" {
			if filter.IsObjectExcluded(filterpkg.ObjectKindView, name) {
				continue
			}
			viewOn, _ := opts.Lookup("viewOn").StringValueOK()
			pipelineJSON, _ := bsonValueToExtJSON(opts.Lookup("pipeline"))
			dump.Views = append(dump.Views, mongoViewDump{
				Name:     name,
				ViewOn:   viewOn,
				Pipeline: pipelineJSON,
			})
			continue
		}

		// Separate validator from collection create options. A validator named by
		// an object_exclude rule is stripped so the collection migrates without it.
		var validatorJSON json.RawMessage
		validatorVal := opts.Lookup("validator")
		if validatorVal.Type == bson.TypeEmbeddedDocument && !filter.IsObjectExcluded(filterpkg.ObjectKindValidator, name) {
			validatorJSON, _ = bsonValueToExtJSON(validatorVal)
		}

		cleanOpts := mongoBSONRemoveKeys(opts, "validator", "validationLevel", "validationAction")
		var optionsJSON json.RawMessage
		if len(cleanOpts) > 4 { // 4 = empty document bytes (just length + terminator)
			optionsJSON, _ = mongoRawToExtJSON(cleanOpts)
		}

		dump.Collections = append(dump.Collections, mongoCollDump{
			Name:      name,
			Options:   optionsJSON,
			Validator: validatorJSON,
		})

		// List indexes, skipping the auto-created _id_ index.
		idxCursor, err := db.Collection(name).Indexes().List(ctx)
		if err != nil {
			return base.Obj{
				Kind: filterpkg.ObjectKindIndex, Owner: db.Name(), Name: name,
			}.Fail(base.ActionList, err)
		}
		var idxDefs []json.RawMessage
		for idxCursor.Next(ctx) {
			idxName, _ := idxCursor.Current.Lookup("name").StringValueOK()
			if idxName == "_id_" {
				continue
			}
			if filter.IsObjectExcludedInTable(filterpkg.ObjectKindIndex, name, idxName) {
				continue
			}
			j, _ := mongoRawToExtJSON(idxCursor.Current)
			idxDefs = append(idxDefs, j)
		}
		idxCursor.Close(ctx)
		if len(idxDefs) > 0 {
			if dump.Indexes == nil {
				dump.Indexes = make(map[string][]json.RawMessage)
			}
			dump.Indexes[name] = idxDefs
		}
	}
	return cursor.Err()
}

// dumpDataViaDriver exports all documents per collection as Extended JSON.
func (d *MongoDBDriver) dumpDataViaDriver(ctx context.Context, db *mongo.Database, filter *filterpkg.DBMSFilterOption, dump *mongoDumpFile) error {
	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "type", Value: "collection"}})
	if err != nil {
		return base.Obj{Kind: base.ObjectKindCollection, Owner: db.Name()}.Fail(base.ActionList, err)
	}
	// Dropped before the loop rather than inside it so the progress counter
	// (Index/Total) counts the collections actually read.
	names = excludeMongoSystemCollections(names)

	for i, name := range names {
		if !mongoIncludeCollection(name, filter) {
			continue
		}
		obj := base.Obj{
			Kind: base.ObjectKindCollection, Owner: db.Name(), Name: name,
			Index: i + 1, Total: len(names),
		}

		// Field exclusion is applied as a projection ({field: 0, ...}); the whole
		// collection is otherwise read.
		findOpts := options.Find()
		if proj := buildMongoProjection(name, filter); proj != nil {
			findOpts.SetProjection(proj)
		}

		// Document exclusion is applied as the query itself, the same slot the
		// relational drivers fill with a WHERE clause.
		rowFilter, err := buildMongoRowFilter(name, filter)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}

		cursor, err := db.Collection(name).Find(ctx, rowFilter, findOpts)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}

		// A document that cannot be read is reported by its position in the
		// collection rather than the collection's position in the dump.
		docNum := 0
		docObj := base.Obj{
			Kind: base.ObjectKindCollection, Owner: db.Name(), Name: name, Unit: "document",
		}
		var docs []json.RawMessage
		for cursor.Next(ctx) {
			docNum++
			j, err := mongoRawToExtJSON(cursor.Current)
			if err != nil {
				cursor.Close(ctx)
				docObj.Index = docNum
				return docObj.Fail(base.ActionReadRows, err)
			}
			docs = append(docs, j)
		}
		cursor.Close(ctx)
		if err := cursor.Err(); err != nil {
			docObj.Index = docNum
			return docObj.Fail(base.ActionReadRows, err)
		}

		if len(docs) > 0 {
			if dump.Data == nil {
				dump.Data = make(map[string][]json.RawMessage)
			}
			dump.Data[name] = docs
		}
	}
	return nil
}

func (d *MongoDBDriver) dumpViaSSH(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer) error {
	cfg := loc.SSHTunnel
	args := d.buildMongodumpArgs(cfg, loc.Database, scope, loc.Filter)
	cmd := "mongodump " + strings.Join(args, " ")

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}

	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, dst); err != nil {
		return &base.OperationError{
			Operation:   base.OperationDump,
			DBMSType:    base.DBMSTypeMongoDB,
			Source:      loc.Database,
			Destination: outputPath,
			Command:     cmd,
			Output:      base.SSHStderr(err),
			Err:         err,
		}
	}
	return nil
}

// =============================================================================
// Restore
// =============================================================================

// Restore imports the file at inputPath into the MongoDB database at loc.
// Direct mode reads a JSON dump file; SSH-tunnel mode pipes mongorestore --archive.
func (d *MongoDBDriver) Restore(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	logger.Info("mongodb restore started", "database", loc.Database, "inputPath", inputPath, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.restoreViaDriver(ctx, loc, inputPath, notify)
	}
	return d.restoreViaSSH(ctx, loc, inputPath, notify)
}

func (d *MongoDBDriver) restoreViaDriver(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read dump file: %w", err)
	}

	var dump mongoDumpFile
	if err := json.Unmarshal(data, &dump); err != nil {
		return fmt.Errorf("parse dump file: %w", err)
	}

	client, err := d.openClient(loc)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)

	db := client.Database(loc.Database)

	// --- Schema: create regular collections ---
	for i, coll := range dump.Collections {
		if err := d.createCollection(ctx, db, coll); err != nil {
			return base.Obj{
				Kind: base.ObjectKindCollection, Owner: loc.Database, Name: coll.Name,
				Index: i + 1, Total: len(dump.Collections),
			}.Fail(base.ActionWriteDDL, err)
		}
		if notify != nil {
			notify(coll.Name)
		}
	}

	// --- Schema: apply validators via collMod ---
	for i, coll := range dump.Collections {
		if len(coll.Validator) == 0 {
			continue
		}
		obj := base.Obj{
			Kind: filterpkg.ObjectKindValidator, Owner: loc.Database, Name: coll.Name,
			Index: i + 1, Total: len(dump.Collections),
		}
		var validator bson.Raw
		if err := bson.UnmarshalExtJSON(coll.Validator, false, &validator); err != nil {
			return obj.Fail(base.ActionWriteDDL, err)
		}
		cmd := bson.D{
			{Key: "collMod", Value: coll.Name},
			{Key: "validator", Value: validator},
		}
		if err := db.RunCommand(ctx, cmd).Err(); err != nil {
			return obj.Fail(base.ActionWriteDDL, err)
		}
	}

	// --- Schema: create views ---
	for i, view := range dump.Views {
		if err := d.createView(ctx, db, view); err != nil {
			return base.Obj{
				Kind: filterpkg.ObjectKindView, Owner: loc.Database, Name: view.Name,
				Index: i + 1, Total: len(dump.Views),
			}.Fail(base.ActionWriteDDL, err)
		}
	}

	// --- Schema: create indexes ---
	for collName, idxDefs := range dump.Indexes {
		if err := d.createIndexes(ctx, db.Collection(collName), idxDefs); err != nil {
			return base.Obj{
				Kind: filterpkg.ObjectKindIndex, Owner: loc.Database, Name: collName,
			}.Fail(base.ActionWriteDDL, err)
		}
	}

	// --- Data: insert documents ---
	for collName, docs := range dump.Data {
		if err := d.insertDocuments(ctx, db, collName, docs); err != nil {
			return base.Obj{
				Kind: base.ObjectKindCollection, Owner: loc.Database, Name: collName,
			}.Fail(base.ActionWriteRows, err)
		}
		if notify != nil {
			notify(collName)
		}
	}
	return nil
}

// createCollection creates a collection using the stored create options.
func (d *MongoDBDriver) createCollection(ctx context.Context, db *mongo.Database, coll mongoCollDump) error {
	if len(coll.Options) == 0 {
		return db.CreateCollection(ctx, coll.Name)
	}
	var optsDoc bson.D
	if err := bson.UnmarshalExtJSON(coll.Options, false, &optsDoc); err != nil || len(optsDoc) == 0 {
		return db.CreateCollection(ctx, coll.Name)
	}
	cmd := bson.D{{Key: "create", Value: coll.Name}}
	cmd = append(cmd, optsDoc...)
	return db.RunCommand(ctx, cmd).Err()
}

// createView recreates a view using the stored viewOn and pipeline.
func (d *MongoDBDriver) createView(ctx context.Context, db *mongo.Database, view mongoViewDump) error {
	var pipeline bson.A
	if len(view.Pipeline) > 0 {
		if err := bson.UnmarshalExtJSON(view.Pipeline, false, &pipeline); err != nil {
			return fmt.Errorf("unmarshal pipeline: %w", err)
		}
	}
	cmd := bson.D{
		{Key: "create", Value: view.Name},
		{Key: "viewOn", Value: view.ViewOn},
		{Key: "pipeline", Value: pipeline},
	}
	return db.RunCommand(ctx, cmd).Err()
}

// createIndexes recreates non-_id indexes from stored Extended JSON index documents.
func (d *MongoDBDriver) createIndexes(ctx context.Context, coll *mongo.Collection, idxDefs []json.RawMessage) error {
	var models []mongo.IndexModel
	for _, idxJSON := range idxDefs {
		var idxDoc bson.Raw
		if err := bson.UnmarshalExtJSON(idxJSON, false, &idxDoc); err != nil {
			return fmt.Errorf("unmarshal index def: %w", err)
		}
		keyVal := idxDoc.Lookup("key")
		if keyVal.Type != bson.TypeEmbeddedDocument {
			continue
		}

		idxOpts := options.Index()
		if name, ok := idxDoc.Lookup("name").StringValueOK(); ok {
			idxOpts.SetName(name)
		}
		if unique, ok := idxDoc.Lookup("unique").BooleanOK(); ok && unique {
			idxOpts.SetUnique(true)
		}
		if sparse, ok := idxDoc.Lookup("sparse").BooleanOK(); ok && sparse {
			idxOpts.SetSparse(true)
		}
		if expireVal, ok := idxDoc.Lookup("expireAfterSeconds").Int32OK(); ok {
			idxOpts.SetExpireAfterSeconds(expireVal)
		}
		pfVal := idxDoc.Lookup("partialFilterExpression")
		if pfVal.Type == bson.TypeEmbeddedDocument {
			idxOpts.SetPartialFilterExpression(pfVal.Document())
		}

		models = append(models, mongo.IndexModel{
			Keys:    keyVal.Document(),
			Options: idxOpts,
		})
	}
	if len(models) == 0 {
		return nil
	}
	_, err := coll.Indexes().CreateMany(ctx, models)
	return err
}

// insertDocuments converts Extended JSON documents to BSON and inserts them.
func (d *MongoDBDriver) insertDocuments(ctx context.Context, db *mongo.Database, collName string, docs []json.RawMessage) error {
	if len(docs) == 0 {
		return nil
	}
	batch := make([]interface{}, 0, len(docs))
	for _, docJSON := range docs {
		var raw bson.Raw
		if err := bson.UnmarshalExtJSON(docJSON, false, &raw); err != nil {
			return fmt.Errorf("unmarshal document: %w", err)
		}
		batch = append(batch, raw)
	}
	_, err := db.Collection(collName).InsertMany(ctx, batch)
	return err
}

func (d *MongoDBDriver) restoreViaSSH(_ context.Context, loc base.DBMSLocation, inputPath string, _ func(string)) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input file: %w", err)
	}
	defer f.Close()

	cmd := d.BuildRestoreCmd(loc)
	if err := base.ExecuteViaSSH(loc.SSHTunnel.SSH, cmd, f, nil); err != nil {
		return &base.OperationError{
			Operation:   base.OperationRestore,
			DBMSType:    base.DBMSTypeMongoDB,
			Destination: loc.Database,
			Command:     cmd,
			Output:      base.SSHStderr(err),
			Err:         err,
		}
	}
	return nil
}

// =============================================================================
// TestConnection
// =============================================================================

// TestConnection verifies the MongoDB instance is reachable and authenticates.
func (d *MongoDBDriver) TestConnection(ctx context.Context, loc base.DBMSLocation) error {
	if loc.IsDirect() {
		client, err := d.openClient(loc)
		if err != nil {
			return err
		}
		defer client.Disconnect(ctx)
		return client.Ping(ctx, nil)
	}

	cfg := loc.SSHTunnel
	uri := d.buildSSHURI(cfg, loc.Database)
	cmd := fmt.Sprintf(`mongosh %s --quiet --eval "db.version()"`, mongoShellQuote(uri))
	var out bytes.Buffer
	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, &out); err != nil {
		return &base.OperationError{
			Operation: base.OperationConnect,
			DBMSType:  base.DBMSTypeMongoDB,
			Source:    loc.Database,
			Command:   cmd,
			Output:    base.SSHStderr(err),
			Err:       err,
		}
	}
	return nil
}

// =============================================================================
// BuildDumpCmd / BuildRestoreCmd
// =============================================================================

// BuildDumpCmd returns the mongodump CLI command used by PipeExecutor (T13).
func (d *MongoDBDriver) BuildDumpCmd(loc base.DBMSLocation, scope string) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	if scope == "" {
		scope = base.ScopeFull
	}
	args := d.buildMongodumpArgs(loc.SSHTunnel, loc.Database, scope, loc.Filter)
	return "mongodump " + strings.Join(args, " ")
}

// BuildRestoreCmd returns the mongorestore CLI command used by PipeExecutor (T13).
func (d *MongoDBDriver) BuildRestoreCmd(loc base.DBMSLocation) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	args := d.buildMongorestoreArgs(loc.SSHTunnel, loc.Database, base.ScopeFull)
	return "mongorestore " + strings.Join(args, " ")
}

// =============================================================================
// CheckTargetEmpty
// =============================================================================

// CheckTargetEmpty returns *TargetNotEmptyError when the database already has collections.
func (d *MongoDBDriver) CheckTargetEmpty(ctx context.Context, loc base.DBMSLocation) error {
	logger.Info("mongodb check target empty", "database", loc.Database, "access", loc.AccessType)
	var count int

	if loc.IsDirect() {
		client, err := d.openClient(loc)
		if err != nil {
			return err
		}
		defer client.Disconnect(ctx)
		names, err := client.Database(loc.Database).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return err
		}
		// A database whose only remaining collection is the server's own (a
		// system.views left by a dropped view, say) holds nothing of the user's
		// and is empty as far as a migration is concerned.
		count = len(excludeMongoSystemCollections(names))
	} else {
		cfg := loc.SSHTunnel
		uri := d.buildSSHURI(cfg, loc.Database)
		// Same rule as the direct path: the server's own collections do not
		// count towards "not empty".
		cmd := fmt.Sprintf(
			`mongosh %s --quiet --eval "db.getCollectionNames().filter(function(c) { return c.indexOf('system.') !== 0; }).length"`,
			mongoShellQuote(uri))
		var out bytes.Buffer
		if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, &out); err != nil {
			return err
		}
		count, _ = strconv.Atoi(strings.TrimSpace(out.String()))
	}

	if count > 0 {
		return &base.TargetNotEmptyError{
			DBMSType:   base.DBMSTypeMongoDB,
			Database:   loc.Database,
			TableCount: count,
		}
	}
	return nil
}

// =============================================================================
// ListDatabases
// =============================================================================

// ListDatabases returns the user databases on loc's server. loc.Database is
// ignored: listDatabases is a server-level command, and it is issued against
// "admin", which is also where the credentials authenticate by default.
func (d *MongoDBDriver) ListDatabases(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	logger.Info("mongodb list databases", "access", loc.AccessType)

	var names []string

	if loc.IsDirect() {
		serverLoc := loc
		serverLoc.Database = mongoAdminDB
		client, err := d.openClient(serverLoc)
		if err != nil {
			return nil, err
		}
		defer client.Disconnect(ctx)

		names, err = client.ListDatabaseNames(ctx, bson.D{})
		if err != nil {
			return nil, fmt.Errorf("list databases: %w", err)
		}
	} else {
		cfg := loc.SSHTunnel
		// nameOnly skips the per-database size accounting the command performs
		// otherwise, which is what makes this a single cheap round trip. The
		// payload travels as sentinel-prefixed JSON because mongosh may mix its
		// banner into stdout, and because a database name can contain a newline.
		eval := `print("` + mongoJSONSentinel + `" + JSON.stringify(` +
			`db.adminCommand({listDatabases: 1, nameOnly: true}).databases.map(function (e) { return e.name; })));`
		// A single eval, so a per-command connection is all this needs.
		if err := d.mongoShellJSON(base.NewSSHExec(cfg.SSH), cfg, mongoAdminDB, eval, &names); err != nil {
			return nil, fmt.Errorf("list databases via SSH: %w", err)
		}
	}

	return base.UserDatabases(base.DBMSTypeMongoDB, names), nil
}

// =============================================================================
// CreateDatabase / DropDatabase
// =============================================================================

// CreateDatabase makes loc.Database exist, as far as MongoDB permits.
//
// MongoDB has no CREATE DATABASE: a database begins to exist when its first
// collection appears. With opt.MongoDB.Collection set the database is
// materialised by creating that empty collection; with it unset the call is a
// documented no-op that reports success, and the database stays invisible to
// ListDatabases until the migration writes to it.
//
// Existence is judged by listDatabases — the same authority ListDatabases uses —
// so, unlike PrepareTarget, MongoDB is not exempt from TargetDatabaseExistsError
// here: a database holding data is reported as existing.
func (d *MongoDBDriver) CreateDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.CreateDatabaseOption) error {
	logger.Info("mongodb create database", "database", loc.Database, "access", loc.AccessType)

	mongoOpt := base.MongoDBCreateOpt(opt)
	collection := mongoOpt.Collection
	if collection == "" && mongoOpt.Collation != nil {
		logger.Warn("mongodb create database ignores collation without a collection to attach it to",
			"database", loc.Database)
	}

	if loc.IsDirect() {
		serverLoc := loc
		serverLoc.Database = mongoAdminDB
		client, err := d.openClient(serverLoc)
		if err != nil {
			return err
		}
		defer client.Disconnect(ctx) //nolint:errcheck — nothing actionable on close

		exists, err := d.databaseExistsDriver(ctx, client, loc.Database)
		if err != nil {
			return err
		}
		if exists {
			return base.CreateExistsResult(opt, base.DBMSTypeMongoDB, loc.Database)
		}
		if collection == "" {
			logger.Info("mongodb create database is a no-op without a collection to create",
				"database", loc.Database)
			return nil
		}
		if err = client.Database(loc.Database).CreateCollection(ctx, collection,
			mongoCreateCollectionOpts(mongoOpt.Collation)); err != nil {
			return fmt.Errorf("createDatabase createCollection %s: %w", collection, err)
		}
		return nil
	}

	// The existence check and the command are two round trips, so they share one
	// connection rather than paying a handshake each.
	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	exists, err := d.databaseExistsSSH(exec, loc.SSHTunnel, loc.Database)
	if err != nil {
		return err
	}
	if exists {
		return base.CreateExistsResult(opt, base.DBMSTypeMongoDB, loc.Database)
	}
	if collection == "" {
		logger.Info("mongodb create database is a no-op without a collection to create",
			"database", loc.Database)
		return nil
	}
	nameLit, err := json.Marshal(collection)
	if err != nil {
		return fmt.Errorf("encode collection name %q: %w", collection, err)
	}
	call, err := mongoCreateCollectionCall(string(nameLit), mongoOpt.Collation)
	if err != nil {
		return err
	}
	if _, err = d.mongoShellQuery(exec, loc.SSHTunnel, loc.Database, call); err != nil {
		return fmt.Errorf("createDatabase createCollection %s via SSH: %w", collection, err)
	}
	return nil
}

// mongoCreateCollectionOpts turns a collation option into the driver's
// createCollection options. A nil collation yields options that set nothing, so
// the collection takes the server's default — returning nil instead would give
// the caller a second call shape to branch on.
func mongoCreateCollectionOpts(c *base.MongoDBCollation) *options.CreateCollectionOptionsBuilder {
	opts := options.CreateCollection()
	if c == nil {
		return opts
	}
	collation := &options.Collation{
		Locale:          c.Locale,
		CaseLevel:       c.CaseLevel,
		NumericOrdering: c.NumericOrdering,
	}
	// Strength is left unset at zero so the server applies its own default of 3,
	// rather than being sent as a level ICU does not define.
	if c.Strength != 0 {
		collation.Strength = c.Strength
	}
	return opts.SetCollation(collation)
}

// mongoCreateCollectionCall renders the createCollection the SSH path runs
// through mongosh. nameLit is the collection name already encoded as a JSON
// string literal.
//
// The collation document is marshalled rather than formatted, so the locale
// cannot break out of the expression. It reaches here having passed
// base.ValidateCreateOption's token check; encoding it keeps that safe however
// the check changes.
func mongoCreateCollectionCall(nameLit string, c *base.MongoDBCollation) (string, error) {
	if c == nil {
		return fmt.Sprintf("db.createCollection(%s)", nameLit), nil
	}
	doc := map[string]any{"locale": c.Locale}
	if c.Strength != 0 {
		doc["strength"] = c.Strength
	}
	if c.CaseLevel {
		doc["caseLevel"] = true
	}
	if c.NumericOrdering {
		doc["numericOrdering"] = true
	}
	collationLit, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("encode collation %+v: %w", *c, err)
	}
	return fmt.Sprintf("db.createCollection(%s, { collation: %s })", nameLit, collationLit), nil
}

// DropDatabase drops loc.Database with the dropDatabase command.
//
// This needs a dbAdmin-level privilege on the database. It is why DropAllObjects
// drops collections one at a time instead: the accounts a migration runs under
// normally hold only readWrite, which is enough to empty a database but not to
// remove it.
func (d *MongoDBDriver) DropDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.DropDatabaseOption) error {
	logger.Info("mongodb drop database", "database", loc.Database, "access", loc.AccessType)

	if loc.IsDirect() {
		serverLoc := loc
		serverLoc.Database = mongoAdminDB
		client, err := d.openClient(serverLoc)
		if err != nil {
			return err
		}
		defer client.Disconnect(ctx) //nolint:errcheck — nothing actionable on close

		exists, err := d.databaseExistsDriver(ctx, client, loc.Database)
		if err != nil {
			return err
		}
		if !exists {
			return base.DropMissingResult(opt, base.DBMSTypeMongoDB, loc.Database)
		}
		if base.DropRequiresEmpty(opt) {
			if err = d.CheckTargetEmpty(ctx, loc); err != nil {
				return err
			}
		}
		if err = client.Database(loc.Database).Drop(ctx); err != nil {
			return fmt.Errorf("dropDatabase: %w", err)
		}
		return nil
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	exists, err := d.databaseExistsSSH(exec, loc.SSHTunnel, loc.Database)
	if err != nil {
		return err
	}
	if !exists {
		return base.DropMissingResult(opt, base.DBMSTypeMongoDB, loc.Database)
	}
	if base.DropRequiresEmpty(opt) {
		if err = d.CheckTargetEmpty(ctx, loc); err != nil {
			return err
		}
	}
	if _, err = d.mongoShellQuery(exec, loc.SSHTunnel, loc.Database, "db.dropDatabase()"); err != nil {
		return fmt.Errorf("dropDatabase via SSH: %w", err)
	}
	return nil
}

// databaseExistsDriver reports whether database is listed by the server. A
// MongoDB database exists exactly when it holds data, so listDatabases is the
// authority — a name with no collections is genuinely not there, which is the
// same ambiguity CheckTargetEmpty documents, resolved in the only direction the
// server offers.
func (d *MongoDBDriver) databaseExistsDriver(ctx context.Context, client *mongo.Client, database string) (bool, error) {
	names, err := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: database}})
	if err != nil {
		return false, fmt.Errorf("check database exists: %w", err)
	}
	return len(names) > 0, nil
}

// databaseExistsSSH is the SSH-tunnel counterpart of databaseExistsDriver. The
// name travels as a JSON literal because a database name may contain characters
// that would otherwise break the surrounding shell script, and the answer comes
// back as sentinel-prefixed JSON because mongosh may mix a banner into stdout.
func (d *MongoDBDriver) databaseExistsSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string) (bool, error) {
	nameLit, err := json.Marshal(database)
	if err != nil {
		return false, fmt.Errorf("encode database name %q: %w", database, err)
	}
	eval := fmt.Sprintf(`print("%s" + JSON.stringify(`+
		`db.adminCommand({listDatabases: 1, nameOnly: true, filter: {name: %s}})`+
		`.databases.map(function (e) { return e.name; })));`, mongoJSONSentinel, nameLit)

	var names []string
	if err := d.mongoShellJSON(exec, cfg, mongoAdminDB, eval, &names); err != nil {
		return false, fmt.Errorf("check database exists via SSH: %w", err)
	}
	return len(names) > 0, nil
}

// =============================================================================
// Inspect
// =============================================================================

// Inspect returns server-level and per-collection metadata for the database.
func (d *MongoDBDriver) Inspect(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	if loc.IsDirect() {
		return d.inspectViaDriver(ctx, loc)
	}
	return d.inspectViaSSH(ctx, loc)
}

func (d *MongoDBDriver) inspectViaDriver(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := mongoMetric(loc)
	client, err := d.openClient(loc)
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(ctx)

	db := client.Database(loc.Database)
	info := &base.DBMSInfo{Database: loc.Database}

	if info.ServerVersion, err = mongoServerVersionViaDriver(ctx, db); err != nil {
		return nil, err
	}

	// Database stats.
	var dbStats bson.M
	if err := db.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&dbStats); err != nil {
		return nil, fmt.Errorf("dbStats: %w", err)
	}
	info.DataSize = mongoBSONInt64(dbStats, "dataSize")
	info.IndexSize = mongoBSONInt64(dbStats, "indexSize")
	info.TotalSize = info.DataSize + info.IndexSize

	// Per-collection stats. ListCollections rather than ListCollectionNames:
	// the default collation lives in each entry's "options" document, which the
	// name-only form does not return. Both issue the same server command, so
	// reading the options costs no extra round trip.
	names, collations, err := d.listCollectionsWithCollation(ctx, db)
	if err != nil {
		return nil, err
	}
	info.TableCount = len(names)

	for _, name := range names {
		var collStats bson.M
		if err := db.RunCommand(ctx, bson.D{
			{Key: "collStats", Value: name},
			{Key: "scale", Value: int32(1)},
		}).Decode(&collStats); err != nil {
			continue
		}
		t := base.TableInfo{
			Name:      name,
			DataSize:  mongoBSONInt64(collStats, "size"),
			IndexSize: mongoBSONInt64(collStats, "totalIndexSize"),
			// collStats' "count" is an estimate that can lag recent
			// inserts/deletes; opt into an exact count via RowCountExact.
			RowCount: mongoBSONInt64(collStats, "count"),
			// Only the locale is carried: it is what identifies the collation,
			// and the remaining fields (strength, caseLevel, …) are defaults the
			// server fills in around it. A collection without an explicit
			// collation leaves this empty.
			Collation: collations[name],
		}
		t.TotalSize = t.DataSize + t.IndexSize

		if m.rowCountExact {
			count, err := db.Collection(name).CountDocuments(ctx, bson.D{})
			if err != nil {
				return nil, base.Obj{
					Kind: base.ObjectKindCollection, Owner: loc.Database, Name: name,
				}.Fail(base.ActionInspect, err)
			}
			t.RowCount = count
		}
		if m.needPerCollectionScan() {
			ti, err := d.describeTableViaDriver(ctx, db, name, m.sampleSize)
			if err == nil {
				if m.columns {
					t.Columns = ti.Columns
				}
				if m.indexes {
					t.Indexes = ti.Indexes
				}
			}
		}
		info.Tables = append(info.Tables, t)
	}

	// Database-wide schema objects (opt-in).
	if m.needSchemaScan() {
		if m.views {
			viewNames, err := db.ListCollectionNames(ctx, bson.D{{Key: "type", Value: "view"}})
			if err != nil {
				return nil, fmt.Errorf("list views: %w", err)
			}
			info.Views = viewNames
		}
		if m.validators {
			validated, err := d.listValidatedCollections(ctx, db)
			if err != nil {
				return nil, err
			}
			info.Validators = validated
		}
	}
	return info, nil
}

// =============================================================================
// CharsetProfile
// =============================================================================

// CharsetProfile returns an empty profile without contacting the server.
//
// MongoDB stores UTF-8 and nothing else, so there is no character set to compare
// and no database-level collation to inherit. A collection's default collation
// does travel to the target, but inside the stored create options the dump
// replays verbatim — not as a name the target has to recognise — so there is
// nothing here that could make a restore fail.
//
// The empty profile is returned rather than an error so a caller can profile any
// location uniformly, as ListSchemas does for the engines without schemas.
// =============================================================================
// ServerVersion
// =============================================================================

// ServerVersion returns what buildInfo reports. See base.DBDriver.
//
// MongoDB creates databases lazily, so loc.Database need not exist: the command
// answers for the server whichever database the session names.
func (d *MongoDBDriver) ServerVersion(ctx context.Context, loc base.DBMSLocation) (string, error) {
	if loc.IsDirect() {
		client, err := d.openClient(loc)
		if err != nil {
			return "", err
		}
		defer client.Disconnect(ctx)
		return mongoServerVersionViaDriver(ctx, client.Database(loc.Database))
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return "", err
	}
	defer exec.Close()
	return d.serverVersionViaSSH(exec, loc.SSHTunnel, loc.Database)
}

// mongoServerVersionViaDriver and serverVersionViaSSH take a connection the
// caller already holds, so an inspection reuses its own rather than dialling
// twice.

func mongoServerVersionViaDriver(ctx context.Context, db *mongo.Database) (string, error) {
	var buildInfo bson.M
	if err := db.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&buildInfo); err != nil {
		return "", fmt.Errorf("buildInfo: %w", err)
	}
	// A build without a version string leaves it empty rather than failing: the
	// caller reads "" as "cannot compare", which is what an unreadable version
	// means.
	ver, _ := buildInfo["version"].(string)
	return strings.TrimSpace(ver), nil
}

func (d *MongoDBDriver) serverVersionViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string) (string, error) {
	ver, err := d.mongoShellQuery(exec, cfg, database, "db.version()")
	if err != nil {
		return "", err
	}
	// mongosh prints the value as a JSON string, quotes included.
	return strings.Trim(strings.TrimSpace(ver), `"`), nil
}

func (d *MongoDBDriver) CharsetProfile(_ context.Context, _ base.DBMSLocation) (*base.CharsetProfile, error) {
	return &base.CharsetProfile{}, nil
}

// listCollectionsWithCollation returns the collection names, sorted as the
// server lists them, along with the default collation locale of each collection
// that declares one. A collection with no collation is absent from the map
// rather than mapped to an empty string, which is the same thing to a caller
// that only reads it back.
//
// It replaces a ListCollectionNames call: the two issue the same server command,
// but only the cursor form exposes the "options" document the collation sits in.
func (d *MongoDBDriver) listCollectionsWithCollation(ctx context.Context, db *mongo.Database) ([]string, map[string]string, error) {
	cursor, err := db.ListCollections(ctx, bson.D{{Key: "type", Value: "collection"}})
	if err != nil {
		return nil, nil, fmt.Errorf("list collections: %w", err)
	}
	defer cursor.Close(ctx)

	var names []string
	collations := make(map[string]string)
	for cursor.Next(ctx) {
		name, ok := cursor.Current.Lookup("name").StringValueOK()
		if !ok || isMongoSystemCollection(name) {
			continue
		}
		names = append(names, name)
		opts, ok := cursor.Current.Lookup("options").DocumentOK()
		if !ok {
			continue
		}
		coll, ok := opts.Lookup("collation").DocumentOK()
		if !ok {
			continue
		}
		if locale, ok := coll.Lookup("locale").StringValueOK(); ok && locale != "" {
			collations[name] = locale
		}
	}
	return names, collations, cursor.Err()
}

// listValidatedCollections returns the names of collections that carry a schema
// validator (options.validator).
func (d *MongoDBDriver) listValidatedCollections(ctx context.Context, db *mongo.Database) ([]string, error) {
	cursor, err := db.ListCollections(ctx, bson.D{{Key: "type", Value: "collection"}})
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	defer cursor.Close(ctx)
	var names []string
	for cursor.Next(ctx) {
		name, _ := cursor.Current.Lookup("name").StringValueOK()
		if isMongoSystemCollection(name) {
			continue
		}
		opts, ok := cursor.Current.Lookup("options").DocumentOK()
		if !ok {
			continue
		}
		if v := opts.Lookup("validator"); len(v.Value) > 0 {
			names = append(names, name)
		}
	}
	return names, cursor.Err()
}

func (d *MongoDBDriver) inspectViaSSH(_ context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := mongoMetric(loc)
	cfg := loc.SSHTunnel
	info := &base.DBMSInfo{Database: loc.Database}

	// One connection for the whole inspection: with per-collection metrics
	// enabled this issues one query per collection, and dialing each time would
	// make the SSH handshake the dominant cost.
	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()

	if info.ServerVersion, err = d.serverVersionViaSSH(exec, cfg, loc.Database); err != nil {
		return nil, err
	}

	// Per-collection stats. The listing is restricted to type:"collection" —
	// getCollectionNames() also returns views, and stats() on a view raises,
	// which would fail the whole Inspect on any database that has one.
	// The count is collStats' estimate by default; RowCountExact swaps in an
	// exact countDocuments().
	countExpr := "s.count"
	if m.rowCountExact {
		countExpr = "db.getCollection(ci.name).countDocuments({})"
	}
	eval := fmt.Sprintf(`var out = [];
db.getCollectionInfos({ type: "collection" }).forEach(function(ci) {
  if (ci.name.indexOf("system.") === 0) { return; }
  var s = db.getCollection(ci.name).stats();
  out.push({
    name: ci.name,
    collation: (ci.options && ci.options.collation) ? ci.options.collation.locale : "",
    count: String(%s),
    dataSize: String(s.size),
    indexSize: String(s.totalIndexSize)
  });
});
print("`+mongoJSONSentinel+`" + JSON.stringify(out));`, countExpr)

	var stats []struct {
		Name      string `json:"name"`
		Collation string `json:"collation"`
		Count     string `json:"count"`
		DataSize  string `json:"dataSize"`
		IndexSize string `json:"indexSize"`
	}
	if err := d.mongoShellJSON(exec, cfg, loc.Database, eval, &stats); err != nil {
		return nil, fmt.Errorf("collection stats: %w", err)
	}
	for _, s := range stats {
		t := base.TableInfo{Name: s.Name, Collation: s.Collation}
		t.RowCount, _ = strconv.ParseInt(s.Count, 10, 64)
		t.DataSize, _ = strconv.ParseInt(s.DataSize, 10, 64)
		t.IndexSize, _ = strconv.ParseInt(s.IndexSize, 10, 64)
		t.TotalSize = t.DataSize + t.IndexSize
		info.Tables = append(info.Tables, t)
		info.DataSize += t.DataSize
		info.IndexSize += t.IndexSize
	}
	info.TotalSize = info.DataSize + info.IndexSize
	info.TableCount = len(info.Tables)

	// Per-collection detail (opt-in). Field inference samples m.sampleSize
	// documents, exactly as the direct path does.
	if m.needPerCollectionScan() {
		for i := range info.Tables {
			t := &info.Tables[i]
			ti, err := d.describeTableViaSSH(exec, loc, t.Name, m.sampleSize)
			if err != nil {
				return nil, err
			}
			if m.columns {
				t.Columns = ti.Columns
			}
			if m.indexes {
				t.Indexes = ti.Indexes
			}
		}
	}

	// Database-wide schema objects (opt-in).
	if m.needSchemaScan() {
		if m.views {
			names, err := d.mongoListNamesViaSSH(exec, cfg, loc.Database,
				`db.getCollectionInfos({ type: "view" }).forEach(function(c) { out.push(c.name); });`)
			if err != nil {
				return nil, fmt.Errorf("list views: %w", err)
			}
			info.Views = names
		}
		if m.validators {
			names, err := d.mongoListNamesViaSSH(exec, cfg, loc.Database,
				`db.getCollectionInfos({ type: "collection" }).forEach(function(c) { if (c.options && c.options.validator) { out.push(c.name); } });`)
			if err != nil {
				return nil, fmt.Errorf("list validators: %w", err)
			}
			info.Validators = names
		}
	}
	return info, nil
}

// mongoListNamesViaSSH runs a body that pushes names onto `out` and returns the
// resulting list. The names travel as JSON rather than as printed lines so that
// a collection name containing a newline cannot split into two entries.
func (d *MongoDBDriver) mongoListNamesViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, body string) ([]string, error) {
	eval := "var out = [];\n" + body + "\nprint(\"" + mongoJSONSentinel + "\" + JSON.stringify(out));"
	var names []string
	if err := d.mongoShellJSON(exec, cfg, database, eval, &names); err != nil {
		return nil, err
	}
	return names, nil
}

// =============================================================================
// DescribeTable
// =============================================================================

// DescribeTable returns column and index metadata for a single MongoDB collection.
// Columns are inferred by sampling the first 100 documents.
func (d *MongoDBDriver) DescribeTable(ctx context.Context, loc base.DBMSLocation, table string) (*base.TableInfo, error) {
	if loc.IsDirect() {
		client, err := d.openClient(loc)
		if err != nil {
			return nil, err
		}
		defer client.Disconnect(ctx)
		return d.describeTableViaDriver(ctx, client.Database(loc.Database), table, DefaultSampleSize)
	}
	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()
	return d.describeTableViaSSH(exec, loc, table, DefaultSampleSize)
}

func (d *MongoDBDriver) describeTableViaDriver(ctx context.Context, db *mongo.Database, table string, sampleSize int) (*base.TableInfo, error) {
	info := &base.TableInfo{Name: table}

	// Collection stats.
	var collStats bson.M
	if err := db.RunCommand(ctx, bson.D{
		{Key: "collStats", Value: table},
		{Key: "scale", Value: int32(1)},
	}).Decode(&collStats); err != nil {
		return nil, fmt.Errorf("collStats(%s): %w", table, err)
	}
	info.DataSize = mongoBSONInt64(collStats, "size")
	info.IndexSize = mongoBSONInt64(collStats, "totalIndexSize")
	info.TotalSize = info.DataSize + info.IndexSize

	// collStats' "count" field can lag behind recent inserts/deletes; use an
	// exact countDocuments so callers aren't misled by a stale count.
	count, err := db.Collection(table).CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("count documents in %s: %w", table, err)
	}
	info.RowCount = count

	// Indexes.
	idxCursor, err := db.Collection(table).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer idxCursor.Close(ctx)
	for idxCursor.Next(ctx) {
		raw := idxCursor.Current
		idxName, _ := raw.Lookup("name").StringValueOK()
		if idxName == "_id_" {
			continue
		}
		isUnique, _ := raw.Lookup("unique").BooleanOK()
		idx := base.IndexInfo{Name: idxName, IsUnique: isUnique, Type: "btree"}

		keyVal := raw.Lookup("key")
		if keyVal.Type == bson.TypeEmbeddedDocument {
			elems, err := keyVal.Document().Elements()
			if err == nil {
				for _, e := range elems {
					idx.Columns = append(idx.Columns, e.Key())
					if t, ok := e.Value().StringValueOK(); ok {
						idx.Type = t // "text", "2dsphere", "hashed"
					}
				}
				if len(elems) > 1 {
					idx.Type = "compound"
				}
			}
		}
		info.Indexes = append(info.Indexes, idx)
	}

	// Field sampling: infer column names and types from the first sampleSize documents.
	cursor, err := db.Collection(table).Find(ctx, bson.D{}, options.Find().SetLimit(int64(sampleSize)))
	if err != nil {
		return info, nil
	}
	defer cursor.Close(ctx)

	var fieldOrder []string
	fieldTypes := make(map[string]string)
	for cursor.Next(ctx) {
		elems, err := cursor.Current.Elements()
		if err != nil {
			continue
		}
		for _, e := range elems {
			key := e.Key()
			typeName := mongoBSONTypeName(e.Value().Type)
			if _, seen := fieldTypes[key]; !seen {
				fieldOrder = append(fieldOrder, key)
				fieldTypes[key] = typeName
			} else if fieldTypes[key] != typeName {
				fieldTypes[key] = "mixed"
			}
		}
	}
	for _, name := range fieldOrder {
		info.Columns = append(info.Columns, base.ColumnInfo{
			Name:     name,
			DataType: fieldTypes[name],
		})
	}
	return info, nil
}

// mongoDetailScript gathers everything describeTable needs for one collection in
// a single mongosh invocation: the document count, the index definitions, and the
// inferred field list. Bundling them matters because every ExecuteViaSSH call
// pays a full TCP dial and SSH handshake, so three separate queries per
// collection would triple the round trips of a wide Inspect.
//
// Field inference mirrors the direct path: fields are ordered by first
// appearance while walking the sample in natural order, and a field seen with
// more than one BSON type is reported as "mixed" (resolved by the caller). The
// type names come from the server's own $type operator rather than from
// JavaScript introspection, which cannot tell int32 from double reliably.
//
// %[1]s is the JSON-quoted collection name, %[2]d the sample size.
const mongoDetailScript = `var c = db.getCollection(%[1]s);
var order = [], seen = {};
c.find({}).limit(%[2]d).forEach(function(doc) {
  Object.keys(doc).forEach(function(k) { if (!seen[k]) { seen[k] = true; order.push(k); } });
});
var types = {};
c.aggregate([
  { $limit: %[2]d },
  { $project: { kv: { $objectToArray: "$$ROOT" } } },
  { $unwind: "$kv" },
  { $group: { _id: "$kv.k", t: { $addToSet: { $type: "$kv.v" } } } }
]).forEach(function(g) { types[g._id] = g.t; });
var fields = [];
order.forEach(function(k) {
  if (types[k]) { fields.push({ name: k, types: types[k] }); delete types[k]; }
});
Object.keys(types).sort().forEach(function(k) { fields.push({ name: k, types: types[k] }); });
var indexes = c.getIndexes().map(function(ix) {
  var keys = [];
  Object.keys(ix.key).forEach(function(k) { keys.push({ k: k, v: ix.key[k] }); });
  return { name: ix.name, unique: ix.unique === true, keys: keys };
});
print("` + mongoJSONSentinel + `" + JSON.stringify({
  count: String(c.countDocuments({})),
  indexes: indexes,
  fields: fields
}));`

// mongoSSHTableDetail is the decoded payload of mongoDetailScript.
type mongoSSHTableDetail struct {
	// Count arrives as a string because mongosh may hand JSON.stringify a BSON
	// Long, which would serialise as an object rather than a number.
	Count   string          `json:"count"`
	Indexes []mongoSSHIndex `json:"indexes"`
	Fields  []mongoSSHField `json:"fields"`
}

type mongoSSHIndex struct {
	Name   string           `json:"name"`
	Unique bool             `json:"unique"`
	Keys   []mongoSSHIdxKey `json:"keys"`
}

type mongoSSHIdxKey struct {
	K string `json:"k"`
	// V is the raw key spec: 1/-1 for an ordinary ascending/descending key, or a
	// string such as "text" or "2dsphere" naming a special index type.
	V json.RawMessage `json:"v"`
}

type mongoSSHField struct {
	Name  string   `json:"name"`
	Types []string `json:"types"`
}

func (d *MongoDBDriver) describeTableViaSSH(exec *base.SSHExec, loc base.DBMSLocation, table string, sampleSize int) (*base.TableInfo, error) {
	cfg := loc.SSHTunnel
	info := &base.TableInfo{Name: table}

	if sampleSize <= 0 {
		sampleSize = DefaultSampleSize
	}
	nameLit, err := json.Marshal(table)
	if err != nil {
		return nil, fmt.Errorf("encode collection name %q: %w", table, err)
	}

	var detail mongoSSHTableDetail
	if err := d.mongoShellJSON(exec, cfg, loc.Database,
		fmt.Sprintf(mongoDetailScript, nameLit, sampleSize), &detail); err != nil {
		return nil, fmt.Errorf("describe collection %s: %w", table, err)
	}

	info.RowCount, _ = strconv.ParseInt(strings.TrimSpace(detail.Count), 10, 64)

	for _, ix := range detail.Indexes {
		// The _id index is implicit and is skipped by the direct path too.
		if ix.Name == "_id_" {
			continue
		}
		idx := base.IndexInfo{Name: ix.Name, IsUnique: ix.Unique, Type: "btree"}
		for _, key := range ix.Keys {
			idx.Columns = append(idx.Columns, key.K)
			var special string
			if err := json.Unmarshal(key.V, &special); err == nil {
				idx.Type = special // "text", "2dsphere", "hashed"
			}
		}
		if len(ix.Keys) > 1 {
			idx.Type = "compound"
		}
		info.Indexes = append(info.Indexes, idx)
	}

	for _, f := range detail.Fields {
		info.Columns = append(info.Columns, base.ColumnInfo{
			Name:     f.Name,
			DataType: mongoFieldTypeName(f.Types),
		})
	}
	return info, nil
}

// mongoFieldTypeName collapses the BSON types a field was seen with into the
// single DataType label the direct path produces: the type itself when
// consistent, "mixed" when the sample disagreed.
func mongoFieldTypeName(types []string) string {
	switch len(types) {
	case 0:
		return ""
	case 1:
		return mongoTypeNameFromServer(types[0])
	default:
		return "mixed"
	}
}

// =============================================================================
// Internal Helpers — Connection
// =============================================================================

// openClient dials the server described by a direct-mode location.
//
// TLS is applied through SetTLSConfig rather than URI parameters. The URI form
// spells the same settings across several options whose names and permitted
// combinations differ between driver releases, while a *tls.Config is the one
// shape every engine here builds from base.TLSSettings — so the five modes
// stay identical on MongoDB without buildMongoURI having to know about them.
func (d *MongoDBDriver) openClient(loc base.DBMSLocation) (*mongo.Client, error) {
	uri := d.buildDirectURI(loc.Direct, loc.Database)
	opts := options.Client().ApplyURI(uri)

	settings, err := base.ResolveTLS(loc.Direct)
	if err != nil {
		return nil, err
	}
	if settings.Mode == base.TLSModePrefer {
		// Refused rather than promoted to require: a client either negotiates
		// TLS or it does not, so silently choosing one of the two would answer
		// a question the caller did not ask. Validation catches this earlier;
		// this covers the calls that reach a driver without a validated model.
		return nil, fmt.Errorf("tlsMode %q is not supported for MongoDB: use %s or %s",
			base.TLSModePrefer, base.TLSModeDisable, base.TLSModeRequire)
	}
	tlsCfg, err := settings.GoTLSConfig()
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}

	client, err := mongo.Connect(opts)
	if err != nil {
		err = base.ExplainTLSError(err, settings.Mode, loc.Direct.Host)
		return nil, fmt.Errorf("mongo.Connect: %w", err)
	}
	return client, nil
}

func (d *MongoDBDriver) buildDirectURI(cfg *base.DirectConfig, database string) string {
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 27017
	}
	return buildMongoURI(cfg.Username, cfg.Password, host, port, database, cfg.AuthSource)
}

func (d *MongoDBDriver) buildSSHURI(cfg *base.SSHTunnelConfig, database string) string {
	host := cfg.DBHost
	if host == "" {
		host = "localhost"
	}
	port := cfg.DBPort
	if port == 0 {
		port = 27017
	}
	return buildMongoURI(cfg.Username, cfg.Password, host, port, database, cfg.AuthSource)
}

// mongoDirectConnection pins the driver to the host it was given.
//
// Without it the driver treats a single seed host as something to discover a
// topology from: it sends hello, and if the server answers with a replica set
// name it switches to replica-set mode and thereafter talks only to the members
// that response listed. Those member names are the ones the set knows itself by,
// which is not how a client outside reaches it:
//
//	server selection error: context deadline exceeded, current topology:
//	{ Type: ReplicaSetNoPrimary, Servers: [{ Addr: 4adonm.vpc.mg.naverncp.com:27017,
//	  Type: Unknown }, ] }
//
// That is an NCP Cloud DB for MongoDB endpoint reached through its public domain:
// the driver dialled the public name, was handed the private one, and spent the
// whole timeout on a host it cannot route to. A managed service behind a public
// endpoint or a NAT does this, and so does every SSH tunnel - the tunnel forwards
// one port, and the member names mean nothing on the local side.
//
// directConnection=true removes the discovery step: Single topology, one server,
// the address the caller named. That matches what this tool is asked to do - move
// data to and from *this* endpoint - and it is also the only thing that works
// through a tunnel, which is why it is not a setting.
//
// ⚠ What it gives up: the driver no longer finds a replica set's primary on its
//
//	own. Reads are unaffected (Single topology sends them to the one server
//	whatever the read preference), but a write needs the endpoint to BE the
//	primary. Naming a secondary as a migration target now fails with "not
//	primary" instead of being quietly redirected.
const mongoDirectConnection = "directConnection=true"

// buildMongoURI assembles a mongodb:// connection string. When credentials are
// provided it appends authSource: without an explicit authSource MongoDB would
// authenticate against the target database (the URI path), but users are usually
// created in the "admin" database, so admin is the default here.
//
// directConnection is always set; see mongoDirectConnection for why.
func buildMongoURI(username, password, host string, port int, database, authSource string) string {
	db := url.PathEscape(database)
	if username == "" {
		return fmt.Sprintf("mongodb://%s:%d/%s?%s", host, port, db, mongoDirectConnection)
	}
	if authSource == "" {
		authSource = "admin"
	}
	return fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s&%s",
		url.QueryEscape(username),
		url.QueryEscape(password),
		host, port, db,
		url.QueryEscape(authSource),
		mongoDirectConnection)
}

// =============================================================================
// Internal Helpers — CLI Command Builders
// =============================================================================

// buildMongodumpArgs builds the mongodump argument list for SSH-tunnel mode.
// Always uses --archive for stdout piping.
func (d *MongoDBDriver) buildMongodumpArgs(cfg *base.SSHTunnelConfig, database, scope string, filter *filterpkg.DBMSFilterOption) []string {
	uri := d.buildSSHURI(cfg, database)
	args := []string{
		"--uri=" + mongoShellQuote(uri),
		"--archive",
	}
	if scope == base.ScopeSchemaOnly {
		args = append(args, "--noData")
	}
	// Field exclusion is impossible with mongodump (it dumps whole documents), so
	// over ssh-tunnel only whole-collection exclusions apply; field rules are
	// rejected by validation before reaching here.
	for _, c := range filter.ExcludedTableNames() {
		args = append(args, "--excludeCollection="+c)
	}
	return args
}

// buildMongorestoreArgs builds the mongorestore argument list for SSH-tunnel mode.
func (d *MongoDBDriver) buildMongorestoreArgs(cfg *base.SSHTunnelConfig, database, scope string) []string {
	uri := d.buildSSHURI(cfg, database)
	args := []string{
		"--uri=" + mongoShellQuote(uri),
		"--archive",
	}
	switch scope {
	case base.ScopeSchemaOnly:
		args = append(args, "--noInsert")
	case base.ScopeDataOnly:
		args = append(args, "--noIndexRestore")
	}
	return args
}

// mongoJSONSentinel prefixes the JSON payload a shell script prints, so the
// result can be picked out of stdout even when mongosh mixes in a banner or a
// deprecation warning.
const mongoJSONSentinel = "DBMSX_JSON:"

// mongoShellJSON runs eval on the remote host and decodes the sentinel-prefixed
// JSON line it prints into dst. Carrying structured results as JSON rather than
// as printed columns keeps collection and field names intact even when they
// contain a tab or a newline.
func (d *MongoDBDriver) mongoShellJSON(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, eval string, dst interface{}) error {
	out, err := d.mongoShellQuery(exec, cfg, database, eval)
	if err != nil {
		return err
	}
	payload, ok := mongoExtractJSON(out)
	if !ok {
		return fmt.Errorf("mongosh returned no %s payload: %s", mongoJSONSentinel, out)
	}
	if err := json.Unmarshal([]byte(payload), dst); err != nil {
		return fmt.Errorf("parse mongosh json output: %w", err)
	}
	return nil
}

// mongoExtractJSON returns the payload of the last sentinel-prefixed line in out.
func mongoExtractJSON(out string) (string, bool) {
	payload, found := "", false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, mongoJSONSentinel) {
			payload = strings.TrimPrefix(line, mongoJSONSentinel)
			found = true
		}
	}
	return payload, found
}

// mongoShellQuery runs a mongosh --eval expression on the remote SSH host
// and returns the trimmed stdout.
func (d *MongoDBDriver) mongoShellQuery(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, eval string) (string, error) {
	uri := d.buildSSHURI(cfg, database)
	cmd := fmt.Sprintf("mongosh %s --quiet --eval %s",
		mongoShellQuote(uri), mongoShellQuote(eval))
	var buf bytes.Buffer
	if err := exec.Run(cmd, nil, &buf); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// =============================================================================
// Internal Helpers — Filter
// =============================================================================

// mongoIncludeCollection returns true unless the collection is wholly excluded,
// either as the server's own bookkeeping or by a filter rule.
func mongoIncludeCollection(name string, filter *filterpkg.DBMSFilterOption) bool {
	return !isMongoSystemCollection(name) && !filter.IsTableExcluded(name)
}

// isMongoSystemCollection reports whether name belongs to the server rather than
// to the user: view definitions live in system.views, profiling output in
// system.profile, stored JavaScript in system.js.
//
// listCollections reports system.views as an ordinary collection ("type":
// "collection"), so a caller that reads the list as user data will try to read
// it — and no built-in role, root included, may run find on a system collection.
// They are the server's own state, recreated as it needs them, and never
// migrate; the SSH path (mongodump and the mongosh inventory) skips them for the
// same reason.
func isMongoSystemCollection(name string) bool {
	return strings.HasPrefix(name, "system.")
}

// excludeMongoSystemCollections returns names without the server's own
// collections, keeping the server's ordering. The input is left untouched:
// callers hand it straight from a list call and some of them read it again.
func excludeMongoSystemCollections(names []string) []string {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if isMongoSystemCollection(name) {
			continue
		}
		kept = append(kept, name)
	}
	return kept
}

// buildMongoRowFilter returns the Find query that drops the documents excluded
// for collection, or an empty document when no row_exclude rule applies.
//
// This is MongoDB's counterpart to the WHERE clause the relational drivers
// attach to their dump SELECT. Each rule's query document describes documents to
// REMOVE, so they are combined under $nor: a document is kept only if it matches
// none of them, which is the AND-of-negations the SQL engines build.
//
// $nor also gives the NULL handling the SQL side has to spell out explicitly. A
// document missing the field a predicate names does not match that predicate, so
// $nor keeps it — the same intent as the relational "(m) IS NULL" term.
func buildMongoRowFilter(collection string, filter *filterpkg.DBMSFilterOption) (bson.D, error) {
	queries := filter.RowExcludeQueries(collection)
	if len(queries) == 0 {
		return bson.D{}, nil
	}

	terms := make(bson.A, 0, len(queries))
	for _, raw := range queries {
		var term bson.D
		// UnmarshalExtJSON accepts both plain JSON and Extended JSON, so a
		// predicate can name a typed value (e.g. {"$date": ...}) when it needs to.
		if err := bson.UnmarshalExtJSON(raw, false, &term); err != nil {
			return nil, fmt.Errorf("row exclusion query for %s is not a valid MongoDB query document: %w", collection, err)
		}
		terms = append(terms, term)
	}
	return bson.D{{Key: "$nor", Value: terms}}, nil
}

// buildMongoProjection returns a MongoDB projection excluding the fields removed
// for collection (e.g. {"password": 0, "ssn": 0}), or nil when no field
// exclusion applies. Keys are sorted for deterministic output.
func buildMongoProjection(collection string, filter *filterpkg.DBMSFilterOption) bson.D {
	excluded := filter.ExcludedColumns(collection)
	if len(excluded) == 0 {
		return nil
	}
	fields := make([]string, 0, len(excluded))
	for f := range excluded {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	proj := make(bson.D, 0, len(fields))
	for _, f := range fields {
		proj = append(proj, bson.E{Key: f, Value: 0})
	}
	return proj
}

// =============================================================================
// Internal Helpers — BSON / JSON Conversion
// =============================================================================

// mongoRawToExtJSON converts a bson.Raw document to relaxed Extended JSON.
func mongoRawToExtJSON(raw bson.Raw) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b, err := bson.MarshalExtJSON(raw, false, false)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// bsonValueToExtJSON converts any bson.RawValue to relaxed Extended JSON by
// wrapping it in a single-field document, marshaling, then extracting the value.
func bsonValueToExtJSON(v bson.RawValue) (json.RawMessage, error) {
	if v.Type == 0 {
		return nil, nil
	}
	wrapper := bson.D{{Key: "_", Value: v}}
	b, err := bson.MarshalExtJSON(wrapper, false, false)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m["_"], nil
}

// mongoBSONRemoveKeys returns a new bson.Raw with the named top-level keys removed.
func mongoBSONRemoveKeys(raw bson.Raw, keys ...string) bson.Raw {
	skip := make(map[string]bool, len(keys))
	for _, k := range keys {
		skip[k] = true
	}
	elems, err := raw.Elements()
	if err != nil {
		return raw
	}
	var d bson.D
	for _, e := range elems {
		if !skip[e.Key()] {
			d = append(d, bson.E{Key: e.Key(), Value: e.Value()})
		}
	}
	if len(d) == 0 {
		return bson.Raw{}
	}
	b, err := bson.Marshal(d)
	if err != nil {
		return raw
	}
	return bson.Raw(b)
}

// mongoBSONInt64 extracts a numeric BSON field from bson.M as int64.
func mongoBSONInt64(m bson.M, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// mongoBSONTypeName maps a bson.Type to a human-readable name for ColumnInfo.DataType.
// Every BSON type in the spec is covered so that field inference never degrades
// to an opaque numeric label, and so the SSH path — which receives MongoDB's own
// $type strings — can be mapped onto exactly this vocabulary
// (see mongoTypeNameFromServer).
func mongoBSONTypeName(t bson.Type) string {
	switch t {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binary"
	case bson.TypeUndefined:
		return "undefined"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeNull:
		return "null"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeDBPointer:
		return "dbPointer"
	case bson.TypeJavaScript:
		return "javascript"
	case bson.TypeSymbol:
		return "symbol"
	case bson.TypeCodeWithScope:
		return "javascriptWithScope"
	case bson.TypeInt32:
		return "int32"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeInt64:
		return "int64"
	case bson.TypeDecimal128:
		return "decimal128"
	case bson.TypeMinKey:
		return "minKey"
	case bson.TypeMaxKey:
		return "maxKey"
	default:
		return fmt.Sprintf("bson(%d)", t)
	}
}

// mongoServerTypeNames renames the type strings MongoDB's $type operator
// produces to the names mongoBSONTypeName returns, so field inference reports
// the same DataType whether it ran through the Go driver or through mongosh.
// Names absent from this table are already identical in both vocabularies.
var mongoServerTypeNames = map[string]string{
	"binData": "binary",
	"int":     "int32",
	"long":    "int64",
	"decimal": "decimal128",
}

// mongoTypeNameFromServer normalises a $type string returned by the server.
func mongoTypeNameFromServer(t string) string {
	if mapped, ok := mongoServerTypeNames[t]; ok {
		return mapped
	}
	return t
}

// mongoShellQuote wraps s in single quotes suitable for a shell argument.
func mongoShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// =============================================================================
// PrepareTarget
// =============================================================================

// ListSchemas reports no schemas: MongoDB has no schema layer between a
// database and its collections. See base.DBDriver.ListSchemas.
func (d *MongoDBDriver) ListSchemas(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	return nil, nil
}

// DropAllObjects empties loc.Database by dropping every collection in it, while
// leaving the database itself intact (the target is assumed pre-provisioned).
// mongorestore's archive format has no per-collection SQL DROP equivalent for
// rollbackDrop to generate, but collections can still be dropped directly via
// the driver or mongosh. Collections are dropped one at a time rather than via
// db.dropDatabase(), which requires a dbAdmin-level privilege beyond the
// readWrite role migration accounts are normally granted.
func (d *MongoDBDriver) DropAllObjects(ctx context.Context, loc base.DBMSLocation) error {
	if loc.IsDirect() {
		client, err := d.openClient(loc)
		if err != nil {
			return err
		}
		defer client.Disconnect(ctx)
		db := client.Database(loc.Database)
		names, err := db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return fmt.Errorf("list collections: %w", err)
		}
		// The server drops its own collections with the objects they describe;
		// dropping system.views directly is refused.
		names = excludeMongoSystemCollections(names)
		for _, name := range names {
			if err := db.Collection(name).Drop(ctx); err != nil {
				return fmt.Errorf("drop collection %s: %w", name, err)
			}
		}
		return nil
	}
	// A single command, so a per-command connection is all this needs.
	_, err := d.mongoShellQuery(base.NewSSHExec(loc.SSHTunnel.SSH), loc.SSHTunnel, loc.Database,
		"db.getCollectionNames().forEach(function(c) { "+
			"if (c.indexOf('system.') === 0) { return; } db.getCollection(c).drop(); })")
	return err
}

// PrepareTarget creates the target database user before migration.
// MongoDB creates databases implicitly on first document insert; this method
// only provisions the user with readWrite access to the target database.
func (d *MongoDBDriver) PrepareTarget(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	if loc.IsDirect() {
		return d.prepareTargetViaDriver(ctx, loc, grant)
	}
	return d.prepareTargetViaSSH(ctx, loc, grant)
}

// prepareTargetViaDriver grants access to the target MongoDB database via the
// Go driver. MongoDB creates databases lazily, so a "not found" state cannot be
// distinguished from an "empty" one (both have zero collections); consequently
// TargetDatabaseNotFoundError does not apply here. The target is instead
// required to be empty (no collections).
func (d *MongoDBDriver) prepareTargetViaDriver(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	client, err := d.openClient(loc)
	if err != nil {
		return fmt.Errorf("prepareTarget open connection: %w", err)
	}
	defer client.Disconnect(ctx) //nolint:errcheck

	// Require the target to be empty before granting access.
	if err := d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	// Create user with readWrite role on the target database.
	cmd := bson.D{
		{Key: "createUser", Value: grant.Username},
		{Key: "pwd", Value: grant.Password},
		{Key: "roles", Value: bson.A{
			bson.D{
				{Key: "role", Value: "readWrite"},
				{Key: "db", Value: loc.Database},
			},
		}},
	}
	if err = client.Database(loc.Database).RunCommand(ctx, cmd).Err(); err != nil {
		return fmt.Errorf("prepareTarget createUser: %w", err)
	}
	return nil
}

// prepareTargetViaSSH grants access to the target MongoDB database via the
// SSH-tunnel CLI. As with the direct path, TargetDatabaseNotFoundError does not
// apply (lazy database creation); the target is instead required to be empty.
func (d *MongoDBDriver) prepareTargetViaSSH(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	cfg := loc.SSHTunnel

	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	// Require the target to be empty before granting access.
	if err := d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	// Create user with readWrite role on the target database.
	createUserEval := fmt.Sprintf(
		`db.getSiblingDB(%s).createUser({user:%s,pwd:%s,roles:[{role:"readWrite",db:%s}]})`,
		mongoShellQuote(loc.Database),
		mongoShellQuote(grant.Username),
		mongoShellQuote(grant.Password),
		mongoShellQuote(loc.Database))
	if _, err := d.mongoShellQuery(exec, cfg, "admin", createUserEval); err != nil {
		return fmt.Errorf("prepareTarget createUser via SSH: %w", err)
	}
	return nil
}
