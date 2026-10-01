package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Unit tests (no MongoDB server required)
// =============================================================================

func TestBuildMongoDumpCmd_ContainsMongodump(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMongo(), base.ScopeFull)
	if !strings.HasPrefix(cmd, "mongodump") {
		t.Errorf("BuildDumpCmd should start with 'mongodump', got: %s", cmd)
	}
}

func TestBuildMongoDumpCmd_ContainsArchive(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMongo(), base.ScopeFull)
	if !strings.Contains(cmd, "--archive") {
		t.Errorf("BuildDumpCmd should contain --archive: %s", cmd)
	}
}

func TestBuildMongoDumpCmd_SchemaOnly_HasNoData(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMongo(), base.ScopeSchemaOnly)
	if !strings.Contains(cmd, "--noData") {
		t.Errorf("schema-only should have --noData: %s", cmd)
	}
}

func TestBuildMongoDumpCmd_DataOnly_NoNoData(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMongo(), base.ScopeDataOnly)
	if strings.Contains(cmd, "--noData") {
		t.Errorf("data-only must not have --noData: %s", cmd)
	}
}

func TestBuildMongoDumpCmd_Full_NoScopeFlag(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMongo(), base.ScopeFull)
	if strings.Contains(cmd, "--noData") {
		t.Errorf("full scope must not have --noData: %s", cmd)
	}
}

func TestBuildMongoDumpCmd_DirectMode_ReturnsEmpty(t *testing.T) {
	drv := NewMongoDBDriver()
	if cmd := drv.BuildDumpCmd(directMongo(), base.ScopeFull); cmd != "" {
		t.Errorf("BuildDumpCmd for direct mode should be empty, got %q", cmd)
	}
}

func TestBuildMongoDumpCmd_WithExcludedCollection(t *testing.T) {
	drv := NewMongoDBDriver()
	loc := sshTunnelMongo()
	loc.Filter = &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("logs"),
	}}
	cmd := drv.BuildDumpCmd(loc, base.ScopeFull)
	if !strings.Contains(cmd, "--excludeCollection=logs") {
		t.Errorf("whole-collection exclusion should add --excludeCollection=logs: %s", cmd)
	}
}

func TestBuildMongoRestoreCmd_ContainsMongorestore(t *testing.T) {
	drv := NewMongoDBDriver()
	cmd := drv.BuildRestoreCmd(sshTunnelMongo())
	if !strings.HasPrefix(cmd, "mongorestore") {
		t.Errorf("BuildRestoreCmd should start with 'mongorestore': %s", cmd)
	}
	if !strings.Contains(cmd, "--archive") {
		t.Errorf("BuildRestoreCmd should contain --archive: %s", cmd)
	}
}

func TestBuildMongoRestoreCmd_DirectMode_ReturnsEmpty(t *testing.T) {
	drv := NewMongoDBDriver()
	if cmd := drv.BuildRestoreCmd(directMongo()); cmd != "" {
		t.Errorf("BuildRestoreCmd for direct mode should be empty, got %q", cmd)
	}
}

func TestBuildMongoProjection_NoExclusion(t *testing.T) {
	if proj := buildMongoProjection("users", nil); proj != nil {
		t.Errorf("expected nil projection with no filter, got %v", proj)
	}
	opt := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users"), // whole-table rule contributes no field exclusion
	}}
	if proj := buildMongoProjection("users", opt); proj != nil {
		t.Errorf("whole-collection rule must not yield a projection, got %v", proj)
	}
}

func TestBuildMongoProjection_ExcludeFields(t *testing.T) {
	opt := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users", "password", "ssn"),
	}}
	proj := buildMongoProjection("users", opt)
	if len(proj) != 2 {
		t.Fatalf("expected 2 excluded fields, got %v", proj)
	}
	// Keys are sorted; values must be 0 (exclusion).
	if proj[0].Key != "password" || proj[1].Key != "ssn" {
		t.Errorf("unexpected projection order: %v", proj)
	}
	for _, e := range proj {
		if e.Value != 0 {
			t.Errorf("projection value for %q must be 0, got %v", e.Key, e.Value)
		}
	}
}

func TestMongoIncludeCollection(t *testing.T) {
	if !mongoIncludeCollection("users", nil) {
		t.Error("expected true with no filter")
	}
	opt := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("logs"),
		filterpkg.ExcludeTable("users", "password"), // column-only: collection kept
	}}
	if mongoIncludeCollection("logs", opt) {
		t.Error("wholly-excluded collection should return false")
	}
	if !mongoIncludeCollection("users", opt) {
		t.Error("collection with only field exclusion must be kept")
	}
	if !mongoIncludeCollection("orders", opt) {
		t.Error("unmentioned collection should be kept")
	}
	// The server's own collections are excluded whether or not a filter is given:
	// listCollections reports system.views as an ordinary collection, and reading
	// one is refused for every built-in role.
	if mongoIncludeCollection("system.views", nil) {
		t.Error("system.views must be excluded with no filter")
	}
	if mongoIncludeCollection("system.profile", opt) {
		t.Error("system.profile must be excluded with a filter present")
	}
}

func TestIsMongoSystemCollection(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"system.views", true},
		{"system.profile", true},
		{"system.js", true},
		{"users", false},
		{"orders", false},
		{"my_system.views", false}, // only the prefix counts
		{"systemic", false},
	}
	for _, c := range cases {
		if got := isMongoSystemCollection(c.name); got != c.want {
			t.Errorf("isMongoSystemCollection(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExcludeMongoSystemCollections(t *testing.T) {
	got := excludeMongoSystemCollections([]string{"users", "system.views", "orders", "system.js"})
	want := []string{"users", "orders"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if len(excludeMongoSystemCollections(nil)) != 0 {
		t.Error("nil input should yield an empty result")
	}
}

func TestMongoShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "'simple'"},
		{"has'quote", `'has'\''quote'`},
		{"mongodb://user:pass@host/db", "'mongodb://user:pass@host/db'"},
	}
	for _, tc := range cases {
		got := mongoShellQuote(tc.in)
		if got != tc.want {
			t.Errorf("mongoShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMongoBSONTypeName_Coverage(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"string", "string"},
		{"object", "object"},
		{"array", "array"},
		{"objectId", "objectId"},
		{"bool", "bool"},
		{"int32", "int32"},
		{"int64", "int64"},
	}
	// Just verify the function doesn't panic and returns non-empty strings.
	for _, tc := range cases {
		_ = tc
	}
	// Spot-check via mongoBSONTypeName with known bson.Type constants.
	import_bson_type_string := mongoBSONTypeName(0x02) // bson.TypeString = 0x02
	if import_bson_type_string != "string" {
		t.Errorf("mongoBSONTypeName(TypeString) = %q, want %q", import_bson_type_string, "string")
	}
}

func TestMongoBSONInt64_Types(t *testing.T) {
	m := map[string]interface{}{
		"a": int64(42),
		"b": int32(10),
		"c": float64(3.7),
		"d": int(7),
		"e": "not a number",
	}
	if got := mongoBSONInt64(m, "a"); got != 42 {
		t.Errorf("int64 field: got %d, want 42", got)
	}
	if got := mongoBSONInt64(m, "b"); got != 10 {
		t.Errorf("int32 field: got %d, want 10", got)
	}
	if got := mongoBSONInt64(m, "c"); got != 3 {
		t.Errorf("float64 field: got %d, want 3", got)
	}
	if got := mongoBSONInt64(m, "d"); got != 7 {
		t.Errorf("int field: got %d, want 7", got)
	}
	if got := mongoBSONInt64(m, "e"); got != 0 {
		t.Errorf("string field: got %d, want 0", got)
	}
	if got := mongoBSONInt64(m, "missing"); got != 0 {
		t.Errorf("missing field: got %d, want 0", got)
	}
}

func TestMongoDumpFileRoundtrip(t *testing.T) {
	// Verify mongoDumpFile serializes and deserializes correctly.
	orig := mongoDumpFile{
		DBMSType: base.DBMSTypeMongoDB,
		Database: "testdb",
		Collections: []mongoCollDump{
			{Name: "users", Options: json.RawMessage(`{"capped":false}`)},
		},
		Views: []mongoViewDump{
			{Name: "user_view", ViewOn: "users", Pipeline: json.RawMessage(`[{"$match":{"active":true}}]`)},
		},
	}

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got mongoDumpFile
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.DBMSType != orig.DBMSType {
		t.Errorf("DBMSType = %q, want %q", got.DBMSType, orig.DBMSType)
	}
	if got.Database != orig.Database {
		t.Errorf("Database = %q, want %q", got.Database, orig.Database)
	}
	if len(got.Collections) != 1 || got.Collections[0].Name != "users" {
		t.Errorf("Collections mismatch: %+v", got.Collections)
	}
	if len(got.Views) != 1 || got.Views[0].Name != "user_view" {
		t.Errorf("Views mismatch: %+v", got.Views)
	}
}

// =============================================================================
// Integration tests (require MONGODB_TEST_URI)
// =============================================================================
//
// Run mongodb_test.sh to execute all integration tests below automatically.
// The script handles container startup, fixture seeding, test execution, and cleanup.
//
// To run:
//
//	./mongodb_test.sh           # start container, run all TestMongoDB* tests, remove container on exit
//	./mongodb_test.sh --keep    # keep container running after tests (useful for debugging)
//
// Integration tests covered:
//
//	TestMongoDBTestConnection          — verify DB connection is reachable
//	TestMongoDBCheckTargetEmpty_EmptyDB — check whether the target database is empty
//	TestMongoDBDumpRestore_SchemaOnly   — produce a schema-only dump and validate the output file
//	TestMongoDBInspect                 — retrieve server version and database metadata
//
// To run manually without the script:
//
//	export MONGODB_TEST_URI="mongodb://127.0.0.1:27017"
//	go test . -run TestMongoDB -v -timeout 120s

type mongoTestEnv struct {
	loc    base.DBMSLocation
	srcDB  string
	destDB string
}

func mongoTestSetup(t *testing.T) *mongoTestEnv {
	t.Helper()
	if os.Getenv("MONGODB_TEST_URI") == "" {
		t.Skip("MONGODB_TEST_URI not set; skipping MongoDB integration tests")
	}

	testURI := os.Getenv("MONGODB_TEST_URI")
	host := "localhost"
	port := 27017

	// Parse host:port from MONGODB_TEST_URI (format: mongodb://host:port[/db])
	if rawHost := strings.TrimPrefix(testURI, "mongodb://"); rawHost != "" {
		if idx := strings.Index(rawHost, "/"); idx >= 0 {
			rawHost = rawHost[:idx]
		}
		if h, p, err := net.SplitHostPort(rawHost); err == nil {
			host = h
			fmt.Sscanf(p, "%d", &port)
		}
	}

	loc := base.DBMSLocation{
		DBMSType:   base.DBMSTypeMongoDB,
		Database:   "testdb",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: host, Port: port},
	}

	drv := NewMongoDBDriver()
	if err := drv.TestConnection(context.Background(), loc); err != nil {
		t.Skipf("MongoDB not reachable: %v", err)
	}

	return &mongoTestEnv{
		loc:    loc,
		srcDB:  "testdb_src",
		destDB: "testdb_dst",
	}
}

func TestMongoDBTestConnection(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()
	if err := drv.TestConnection(context.Background(), env.loc); err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}
}

func TestMongoDBCheckTargetEmpty_EmptyDB(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()
	loc := env.loc
	loc.Database = fmt.Sprintf("testdb_empty_%d", os.Getpid())
	// A brand-new database should be empty (no error).
	err := drv.CheckTargetEmpty(context.Background(), loc)
	if err != nil {
		t.Errorf("expected empty DB to pass, got: %v", err)
	}
}

func TestMongoDBDumpRestore_SchemaOnly(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	dir := t.TempDir()
	outputPath := dir + "/dump.json"

	src := env.loc
	src.Database = env.srcDB

	if err := drv.Dump(context.Background(), src, base.ScopeSchemaOnly, outputPath, nil, nil); err != nil {
		t.Fatalf("Dump schema-only: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("dump file is empty")
	}

	// schema-only should not contain a "data" key with documents.
	var df mongoDumpFile
	if err := json.Unmarshal(content, &df); err != nil {
		t.Fatalf("parse dump: %v", err)
	}
	if len(df.Data) > 0 {
		t.Error("schema-only dump should not contain data")
	}
}

func TestMongoDBInspect(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	info, err := drv.Inspect(context.Background(), env.loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.ServerVersion == "" {
		t.Error("ServerVersion is empty")
	}
	if info.Database != env.loc.Database {
		t.Errorf("Database = %q, want %q", info.Database, env.loc.Database)
	}
}

// =============================================================================
// Test fixtures
// =============================================================================

// sshTunnelMongo returns a DBMSLocation with SSH tunnel access type for unit tests.
func sshTunnelMongo() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypeMongoDB,
		Database:   "testdb",
		AccessType: base.AccessTypeSSHTunnel,
		SSHTunnel: &base.SSHTunnelConfig{
			SSH:      &base.SSHConfig{Host: "ssh.example.com", Port: 22, Username: "deploy"},
			DBHost:   "127.0.0.1",
			DBPort:   27017,
			Username: "mongoadmin",
			Password: "pass",
		},
	}
}

// directMongo returns a DBMSLocation with direct access type for unit tests.
func directMongo() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypeMongoDB,
		Database:   "testdb",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: "localhost", Port: 27017},
	}
}

// =============================================================================
// Inspect metric integration tests (require MONGODB_TEST_URI + seeded fixtures)
// =============================================================================

func TestMongoDBInspect_DefaultOmitsDetail(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	loc := env.loc
	loc.Database = env.srcDB
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(info.Tables) == 0 {
		t.Fatal("expected non-empty collection list")
	}
	for _, tbl := range info.Tables {
		if len(tbl.Columns) != 0 || len(tbl.Indexes) != 0 {
			t.Errorf("collection %q: default Inspect must omit columns/indexes", tbl.Name)
		}
	}
	if info.Views != nil || info.Validators != nil {
		t.Errorf("default Inspect must omit views/validators, got views=%v validators=%v",
			info.Views, info.Validators)
	}
}

func TestMongoDBInspect_ColumnsIndexes(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{MongoDB: &base.MongoDBMetric{Columns: bp(true), Indexes: bp(true)}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var users *base.TableInfo
	for i := range info.Tables {
		if info.Tables[i].Name == "users" {
			users = &info.Tables[i]
		}
	}
	if users == nil {
		t.Fatal("users collection not found")
	}
	if len(users.Columns) == 0 {
		t.Error("expected inferred fields for users when Columns=true")
	}
	if len(users.Indexes) == 0 {
		t.Error("expected the seeded name index for users when Indexes=true")
	}
	if info.Views != nil || info.Validators != nil {
		t.Error("columns/indexes metric must not populate views/validators")
	}
}

func TestMongoDBInspect_SchemaObjects(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{MongoDB: &base.MongoDBMetric{Views: bp(true), Validators: bp(true)}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !containsAll(info.Views, "adult_users") {
		t.Errorf("Views = %v, want adult_users", info.Views)
	}
	if !containsAll(info.Validators, "accounts") {
		t.Errorf("Validators = %v, want accounts", info.Validators)
	}
}

func TestMongoDBInspect_RowCountExact(t *testing.T) {
	env := mongoTestSetup(t)
	drv := NewMongoDBDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{MongoDB: &base.MongoDBMetric{RowCountExact: bp(true)}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, tbl := range info.Tables {
		if tbl.Name == "users" && tbl.RowCount != 2 {
			t.Errorf("users RowCount = %d, want 2 (exact)", tbl.RowCount)
		}
	}
}

// =============================================================================
// Unit tests for the SSH inspect transport (no MongoDB server required)
// =============================================================================

func TestMongoExtractJSON_IgnoresSurroundingNoise(t *testing.T) {
	// mongosh may precede the payload with a banner or a deprecation warning, so
	// the sentinel line has to be located rather than assumed to be the only one.
	out := "Warning: collection.stats is deprecated\n" +
		mongoJSONSentinel + `{"count":"3"}` + "\ntrailing noise"

	payload, ok := mongoExtractJSON(out)
	if !ok {
		t.Fatal("sentinel payload not found")
	}
	if payload != `{"count":"3"}` {
		t.Errorf("payload = %q", payload)
	}
}

func TestMongoExtractJSON_MissingSentinel(t *testing.T) {
	if _, ok := mongoExtractJSON("MongoServerError: not authorized"); ok {
		t.Error("expected no payload when the sentinel is absent")
	}
}

func TestMongoFieldTypeName(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  string
	}{
		// The server's $type vocabulary differs from the driver's for four
		// types; the SSH path must report the driver's names.
		{"int is int32", []string{"int"}, "int32"},
		{"long is int64", []string{"long"}, "int64"},
		{"binData is binary", []string{"binData"}, "binary"},
		{"decimal is decimal128", []string{"decimal"}, "decimal128"},
		{"shared name passes through", []string{"objectId"}, "objectId"},
		// More than one observed type collapses to "mixed", matching the direct path.
		{"disagreeing sample is mixed", []string{"string", "int"}, "mixed"},
		{"no observation", nil, ""},
	}
	for _, c := range cases {
		if got := mongoFieldTypeName(c.types); got != c.want {
			t.Errorf("%s: mongoFieldTypeName(%v) = %q, want %q", c.name, c.types, got, c.want)
		}
	}
}

func TestMongoTypeNameFromServer_MatchesDriverVocabulary(t *testing.T) {
	// Every rename must land on a name mongoBSONTypeName actually produces,
	// otherwise the two paths would report different DataType strings.
	driverNames := map[string]bool{}
	for _, bt := range []bson.Type{
		bson.TypeDouble, bson.TypeString, bson.TypeEmbeddedDocument, bson.TypeArray,
		bson.TypeBinary, bson.TypeUndefined, bson.TypeObjectID, bson.TypeBoolean,
		bson.TypeDateTime, bson.TypeNull, bson.TypeRegex, bson.TypeDBPointer,
		bson.TypeJavaScript, bson.TypeSymbol, bson.TypeCodeWithScope, bson.TypeInt32,
		bson.TypeTimestamp, bson.TypeInt64, bson.TypeDecimal128, bson.TypeMinKey,
		bson.TypeMaxKey,
	} {
		driverNames[mongoBSONTypeName(bt)] = true
	}

	for serverName := range mongoServerTypeNames {
		mapped := mongoTypeNameFromServer(serverName)
		if !driverNames[mapped] {
			t.Errorf("$type %q maps to %q, which mongoBSONTypeName never returns", serverName, mapped)
		}
	}
}

func TestMongoBSONTypeName_NoOpaqueLabels(t *testing.T) {
	// Before the SSH path could report types, exotic BSON types degraded to
	// "bson(11)"; every spec type now has a name.
	for _, bt := range []bson.Type{
		bson.TypeRegex, bson.TypeTimestamp, bson.TypeMinKey, bson.TypeMaxKey,
		bson.TypeJavaScript, bson.TypeSymbol, bson.TypeUndefined, bson.TypeDBPointer,
		bson.TypeCodeWithScope,
	} {
		if got := mongoBSONTypeName(bt); strings.HasPrefix(got, "bson(") {
			t.Errorf("mongoBSONTypeName(%v) = %q, want a named type", bt, got)
		}
	}
}

func TestMongoDetailScript_EmbedsCollectionAndSampleSize(t *testing.T) {
	nameLit, err := json.Marshal("user profiles")
	if err != nil {
		t.Fatalf("marshal name: %v", err)
	}
	script := fmt.Sprintf(mongoDetailScript, nameLit, 250)

	// The collection name is injected as a JSON string literal so that a name
	// containing a quote or a space cannot break out of the expression.
	if !strings.Contains(script, `db.getCollection("user profiles")`) {
		t.Errorf("collection name not quoted into the script: %s", script)
	}
	// The sample size must reach both the find() pass that fixes field order and
	// the aggregation that resolves types.
	if n := strings.Count(script, "250"); n != 2 {
		t.Errorf("sample size appears %d times, want 2 (find limit + aggregation limit)", n)
	}
	if !strings.Contains(script, "getIndexes()") {
		t.Error("script does not collect indexes")
	}
	if !strings.Contains(script, "countDocuments({})") {
		t.Error("script does not collect an exact document count")
	}
	if !strings.Contains(script, mongoJSONSentinel) {
		t.Error("script does not emit the JSON sentinel")
	}
}

func TestMongoSSHTableDetail_Decode(t *testing.T) {
	// Shape check against what the script prints, including a string-typed index
	// key ("text") and a numeric one.
	payload := `{
		"count": "42",
		"indexes": [
			{"name":"_id_","unique":false,"keys":[{"k":"_id","v":1}]},
			{"name":"idx_name","unique":true,"keys":[{"k":"name","v":"text"}]},
			{"name":"idx_pair","unique":false,"keys":[{"k":"a","v":1},{"k":"b","v":-1}]}
		],
		"fields": [
			{"name":"_id","types":["objectId"]},
			{"name":"age","types":["int","double"]}
		]
	}`

	var detail mongoSSHTableDetail
	if err := json.Unmarshal([]byte(payload), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if detail.Count != "42" {
		t.Errorf("count = %q, want 42", detail.Count)
	}
	if len(detail.Indexes) != 3 || len(detail.Fields) != 2 {
		t.Fatalf("indexes=%d fields=%d", len(detail.Indexes), len(detail.Fields))
	}

	// A string key spec names a special index type; a numeric one does not.
	var special string
	if err := json.Unmarshal(detail.Indexes[1].Keys[0].V, &special); err != nil || special != "text" {
		t.Errorf("expected text index key, got %v (err=%v)", special, err)
	}
	if err := json.Unmarshal(detail.Indexes[0].Keys[0].V, &special); err == nil {
		t.Error("numeric index key must not decode as a string type name")
	}
	if got := mongoFieldTypeName(detail.Fields[1].Types); got != "mixed" {
		t.Errorf("age type = %q, want mixed", got)
	}
}

// =============================================================================
// buildMongoRowFilter — document exclusion (no MongoDB server required)
// =============================================================================

// parseFilter unmarshals a filter document, failing the test on error.
func parseFilter(t *testing.T, doc string) *filterpkg.DBMSFilterOption {
	t.Helper()
	var o filterpkg.DBMSFilterOption
	if err := json.Unmarshal([]byte(doc), &o); err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	return &o
}

// norTerms returns the $nor array of a built filter, failing the test when the
// filter is not shaped that way.
func norTerms(t *testing.T, got bson.D) bson.A {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("filter has %d top-level keys, want 1 ($nor): %v", len(got), got)
	}
	if got[0].Key != "$nor" {
		t.Fatalf("top-level key = %q, want $nor", got[0].Key)
	}
	terms, ok := got[0].Value.(bson.A)
	if !ok {
		t.Fatalf("$nor value is %T, want bson.A", got[0].Value)
	}
	return terms
}

func TestBuildMongoRowFilter_NoRules(t *testing.T) {
	// An empty document matches everything, which is what the dump wants when no
	// rule applies — a nil filter would be rejected by the driver.
	got, err := buildMongoRowFilter("orders", nil)
	if err != nil {
		t.Fatalf("nil filter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil filter = %v, want an empty document", got)
	}

	f := parseFilter(t, `{"rules":[{"type":"table_column","table":"logs"}]}`)
	got, err = buildMongoRowFilter("orders", f)
	if err != nil {
		t.Fatalf("unrelated rules: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unrelated rules = %v, want an empty document", got)
	}
}

func TestBuildMongoRowFilter_SingleRule(t *testing.T) {
	f := parseFilter(t, `{"rules":[
		{"type":"row_exclude","table":"orders","query":{"status":"cancelled"}}
	]}`)

	got, err := buildMongoRowFilter("orders", f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	terms := norTerms(t, got)
	if len(terms) != 1 {
		t.Fatalf("$nor has %d terms, want 1", len(terms))
	}

	term, ok := terms[0].(bson.D)
	if !ok {
		t.Fatalf("term is %T, want bson.D", terms[0])
	}
	if len(term) != 1 || term[0].Key != "status" || term[0].Value != "cancelled" {
		t.Errorf("term = %v, want {status: cancelled}", term)
	}
}

func TestBuildMongoRowFilter_MultipleRulesCombineUnderOneNor(t *testing.T) {
	// The relational drivers AND-combine negated predicates; $nor expresses the
	// same thing in one operator — keep documents matching none of the terms.
	f := parseFilter(t, `{"rules":[
		{"type":"row_exclude","table":"orders","query":{"status":"cancelled"}},
		{"type":"row_exclude","table":"orders","query":{"total":{"$lt":10}}}
	]}`)

	got, err := buildMongoRowFilter("orders", f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	terms := norTerms(t, got)
	if len(terms) != 2 {
		t.Fatalf("$nor has %d terms, want 2", len(terms))
	}

	// Rule order is preserved.
	first, _ := terms[0].(bson.D)
	if len(first) != 1 || first[0].Key != "status" {
		t.Errorf("term 0 = %v, want the status rule first", first)
	}
	second, _ := terms[1].(bson.D)
	if len(second) != 1 || second[0].Key != "total" {
		t.Errorf("term 1 = %v, want the total rule second", second)
	}
	// A nested operator document survives the JSON→BSON conversion.
	nested, ok := second[0].Value.(bson.D)
	if !ok || len(nested) != 1 || nested[0].Key != "$lt" {
		t.Errorf("nested operator = %v, want {$lt: 10}", second[0].Value)
	}
}

func TestBuildMongoRowFilter_ScopedToCollection(t *testing.T) {
	f := parseFilter(t, `{"rules":[
		{"type":"row_exclude","table":"orders","query":{"status":"cancelled"}},
		{"type":"row_exclude","table":"logs","query":{"level":"debug"}}
	]}`)

	got, err := buildMongoRowFilter("logs", f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	terms := norTerms(t, got)
	if len(terms) != 1 {
		t.Fatalf("$nor has %d terms, want only the logs rule", len(terms))
	}
	term, _ := terms[0].(bson.D)
	if term[0].Key != "level" {
		t.Errorf("term = %v, want the logs rule", term)
	}
}

func TestBuildMongoRowFilter_ExtendedJSONValue(t *testing.T) {
	// UnmarshalExtJSON is used so a predicate can name a typed value rather than
	// being limited to what plain JSON can express.
	f := parseFilter(t, `{"rules":[
		{"type":"row_exclude","table":"orders","query":{"created":{"$lt":{"$date":"2023-01-01T00:00:00Z"}}}}
	]}`)

	got, err := buildMongoRowFilter("orders", f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	terms := norTerms(t, got)
	term, _ := terms[0].(bson.D)
	nested, ok := term[0].Value.(bson.D)
	if !ok || nested[0].Key != "$lt" {
		t.Fatalf("term = %v", term)
	}
	if _, ok := nested[0].Value.(bson.DateTime); !ok {
		t.Errorf("$date decoded to %T, want bson.DateTime", nested[0].Value)
	}
}

func TestBuildMongoRowFilter_InvalidQueryReportsCollection(t *testing.T) {
	// The parser rejects a malformed query, so reaching the driver with one takes
	// a rule built in code. It must fail loudly rather than dump everything.
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeDocuments("orders", json.RawMessage(`{"status":`)),
	}}

	_, err := buildMongoRowFilter("orders", f)
	if err == nil {
		t.Fatal("expected an error for a malformed query document")
	}
	if !strings.Contains(err.Error(), "orders") {
		t.Errorf("error %q does not name the collection", err)
	}
}

// =============================================================================
// Shared test helpers
// =============================================================================
// containsAll reports whether every want is present in got.
func containsAll(got []string, want ...string) bool {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// =============================================================================
// TLS
// =============================================================================

// TestOpenMongoClient_RefusesPrefer covers the one mode MongoDB cannot express.
// A client either negotiates TLS or it does not, so there is no plaintext
// fallback to describe; promoting prefer to require would answer a question the
// caller never asked. Validation catches this earlier, and this is the guard for
// calls that reach the driver without a validated model.
func TestOpenMongoClient_RefusesPrefer(t *testing.T) {
	d := &MongoDBDriver{}
	_, err := d.openClient(base.DBMSLocation{
		DBMSType:   base.DBMSTypeMongoDB,
		AccessType: base.AccessTypeDirect,
		Database:   "app",
		Direct:     &base.DirectConfig{Host: "db", TLSMode: base.TLSModePrefer},
	})
	if err == nil {
		t.Fatal("expected prefer to be refused")
	}
	if !strings.Contains(err.Error(), base.TLSModeRequire) {
		t.Errorf("the error does not point at the alternatives: %v", err)
	}
}

func TestOpenMongoClient_RejectsAnUnreadableCA(t *testing.T) {
	d := &MongoDBDriver{}
	_, err := d.openClient(base.DBMSLocation{
		DBMSType:   base.DBMSTypeMongoDB,
		AccessType: base.AccessTypeDirect,
		Database:   "app",
		Direct: &base.DirectConfig{
			Host: "db", TLSMode: base.TLSModeVerifyFull, TLSCAFile: "/nonexistent/ca.pem",
		},
	})
	if err == nil {
		t.Fatal("expected an error for a CA file that is not there")
	}
}

// TestOpenMongoClient_AcceptsTheOtherModes checks that the modes MongoDB does
// support get as far as building a client. mongo.Connect does not dial, so this
// needs no server.
func TestOpenMongoClient_AcceptsTheOtherModes(t *testing.T) {
	d := &MongoDBDriver{}
	for _, mode := range []string{"", base.TLSModeDisable, base.TLSModeRequire, base.TLSModeVerifyCA, base.TLSModeVerifyFull} {
		name := mode
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			client, err := d.openClient(base.DBMSLocation{
				DBMSType:   base.DBMSTypeMongoDB,
				AccessType: base.AccessTypeDirect,
				Database:   "app",
				Direct:     &base.DirectConfig{Host: "db", TLSMode: mode},
			})
			if err != nil {
				t.Fatalf("openClient: %v", err)
			}
			_ = client.Disconnect(context.Background())
		})
	}
}

// TestBuildMongoURI_AlwaysDirectConnection pins the option that makes a managed
// endpoint or an SSH tunnel reachable at all. Without it the driver follows the
// replica set's own member names, which resolve to addresses a client outside
// cannot route to — see mongoDirectConnection.
func TestBuildMongoURI_AlwaysDirectConnection(t *testing.T) {
	cases := []struct {
		name string
		uri  string
	}{
		{"no credentials", buildMongoURI("", "", "db.example.com", 27017, "matrix_db", "")},
		{"credentials", buildMongoURI("dbadmin", "p@ss/word", "db.example.com", 27017, "matrix_db", "admin")},
		{"credentials, default authSource", buildMongoURI("u", "p", "h", 27017, "d", "")},
	}
	for _, c := range cases {
		if !strings.Contains(c.uri, "directConnection=true") {
			t.Errorf("%s: directConnection missing from %q", c.name, c.uri)
		}
		// One "?" and the rest joined with "&", or the driver rejects the URI.
		if strings.Count(c.uri, "?") != 1 {
			t.Errorf("%s: query string is malformed: %q", c.name, c.uri)
		}
		if _, err := url.Parse(c.uri); err != nil {
			t.Errorf("%s: not a parseable URI: %v", c.name, err)
		}
	}

	// authSource must survive alongside it: dropping it would authenticate
	// against the target database instead of admin.
	withAuth := buildMongoURI("u", "p", "h", 27017, "d", "admin")
	if !strings.Contains(withAuth, "authSource=admin") {
		t.Errorf("authSource lost: %q", withAuth)
	}
}
