// Package database loads the shop_db fixtures into provisioned databases using
// native Go drivers (no CLI clients). SQL fixtures are executed via engine-aware
// statement splitters; the MongoDB fixture is injected from Extended JSON.
package database

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

//go:embed shop_db_mysql.sql
var sqlMySQL string

//go:embed shop_db_mariadb.sql
var sqlMariaDB string

//go:embed shop_db_pg.sql
var sqlPostgres string

//go:embed shop_db_mongo.json
var mongoJSON []byte

// ShopDBName is the fixture database name.
const ShopDBName = "shop_db"

// ErrDataExists indicates the target database already contains shop_db data.
var ErrDataExists = errors.New("database already contains shop_db data")

// Engine identifies a target database engine.
type Engine string

const (
	MySQL   Engine = "mysql"
	MariaDB Engine = "mariadb"
	// "postgresql" is cb-spider's canonical engine name, and what this
	// environment uses throughout - state files, .env keys, security group
	// names. The lib/pq driver name stays "postgres"; that is a different
	// identifier and is spelled out at each sql.Open.
	Postgres Engine = "postgresql"
	MongoDB  Engine = "mongodb"
)

// Config describes a target database connection.
type Config struct {
	Engine   Engine
	Host     string
	Port     string
	User     string
	Password string
	// Database is the target database to load into — the provisioned DB (e.g.
	// tofu output db_name, "testdb"). The fixtures carry no CREATE DATABASE or
	// USE, so the schema lands here. Empty falls back to shop_db.
	Database string
	// Strict, when true, aborts on the first SQL statement error. When false
	// (default) statement errors are logged and loading continues (RDS quirks:
	// event_scheduler / log_bin_trust_function_creators).
	Strict bool
}

// targetDB returns the database to load into (Config.Database or shop_db fallback).
func targetDB(cfg Config) string {
	if cfg.Database != "" {
		return cfg.Database
	}
	return ShopDBName
}

// LoadShopDB loads the shop_db fixture for the configured engine.
func LoadShopDB(ctx context.Context, cfg Config) error {
	switch cfg.Engine {
	case MySQL:
		return loadMySQLFamily(ctx, cfg, sqlMySQL)
	case MariaDB:
		return loadMySQLFamily(ctx, cfg, sqlMariaDB)
	case Postgres:
		return loadPostgres(ctx, cfg)
	case MongoDB:
		return loadMongo(ctx, cfg)
	default:
		return fmt.Errorf("unsupported engine %q", cfg.Engine)
	}
}

// CheckExisting returns ErrDataExists if the target already contains shop_db data.
func CheckExisting(ctx context.Context, cfg Config) error {
	switch cfg.Engine {
	case MySQL, MariaDB:
		return checkMySQLFamily(ctx, cfg)
	case Postgres:
		return checkPostgres(ctx, cfg)
	case MongoDB:
		return checkMongo(ctx, cfg)
	default:
		return fmt.Errorf("unsupported engine %q", cfg.Engine)
	}
}

// ---------------------------------------------------------------------------
// MySQL / MariaDB
// ---------------------------------------------------------------------------

func mysqlDSN(cfg Config, dbName string) string {
	return mysqlConfig(cfg, dbName).FormatDSN()
}

func mysqlConfig(cfg Config, dbName string) *mysql.Config {
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = cfg.Host + ":" + cfg.Port
	mc.DBName = dbName
	mc.TLSConfig = "preferred"
	mc.AllowNativePasswords = true
	mc.MultiStatements = false
	mc.Timeout = 20 * time.Second
	mc.Params = map[string]string{"charset": "utf8mb4"}
	return mc
}

func loadMySQLFamily(ctx context.Context, cfg Config, script string) error {
	dbName := targetDB(cfg)
	// Connect directly to the target DB (must already exist, e.g. tofu testdb);
	// the fixtures contain no CREATE DATABASE / USE.
	db, err := sql.Open("mysql", mysqlDSN(cfg, dbName))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect %s/%s: %w", cfg.Host, dbName, err)
	}
	// Single connection keeps session state consistent across statements.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	stmts := splitMySQL(script)
	failed := 0
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			if cfg.Strict {
				return fmt.Errorf("exec failed: %w\n--- statement ---\n%s", err, truncate(s, 200))
			}
			failed++
			log.Printf("[warn] mysql stmt failed (continuing): %v | %s", err, truncate(s, 120))
		}
	}
	if failed > 0 {
		log.Printf("[info] mysql/mariadb load finished with %d/%d statement warnings", failed, len(stmts))
	}
	return nil
}

func checkMySQLFamily(ctx context.Context, cfg Config) error {
	db, err := sql.Open("mysql", mysqlDSN(cfg, ""))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect %s: %w", cfg.Host, err)
	}
	dbName := targetDB(cfg)
	var n int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", dbName).Scan(&n)
	if err != nil {
		return fmt.Errorf("existence query: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %s has %d tables on %s", ErrDataExists, dbName, n, cfg.Host)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PostgreSQL
// ---------------------------------------------------------------------------

func pgDSN(cfg Config, dbName string) string {
	// key=value form avoids URL-escaping the password.
	// sslmode=prefer negotiates TLS opportunistically and falls back to plaintext,
	// mirroring the mysql driver's TLSConfig="preferred". This keeps every engine
	// on the same policy and works against both TLS-enabled managed instances and
	// self-hosted ones without TLS.
	//
	// options sets search_path for every connection the pool opens. Running
	// "SET search_path" after connecting would only affect whichever connection
	// happened to serve that statement. The value is quoted because it contains a
	// space; the schema name itself is a plain identifier (see pgSchema).
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=prefer connect_timeout=20 options='-c search_path=%s'",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, dbName, pgSchema(cfg))
}

// pgSchema returns the schema the fixtures live in, which is the database name.
//
// It is deliberately not "public". On NCP the managed PostgreSQL master account owns
// the database but has neither USAGE nor CREATE on schema public - that schema is
// owned by postgres with the default PUBLIC grant revoked (nspacl postgres=UC/postgres),
// and the account cannot grant itself anything there. Even a bare
// "SELECT to_regclass('public.products')" fails with 42501. Owning the database is
// enough to create a schema, so the fixtures get their own.
//
// Naming it after the database keeps one identifier from .env describing the fixture
// container on every engine: MySQL and MariaDB have no separate schema layer, MongoDB
// has none either, and this makes PostgreSQL line up with them.
func pgSchema(cfg Config) string {
	return targetDB(cfg)
}

func loadPostgres(ctx context.Context, cfg Config) error {
	dbName := targetDB(cfg)
	// The fixtures carry no DROP/CREATE DATABASE and no \c — load DDL/DML
	// directly into the existing target DB (e.g. tofu testdb).
	target, err := sql.Open("postgres", pgDSN(cfg, dbName))
	if err != nil {
		return err
	}
	defer target.Close()
	if err := target.PingContext(ctx); err != nil {
		return fmt.Errorf("connect postgres(%s) %s: %w", dbName, cfg.Host, err)
	}
	if err := ensurePGSchema(ctx, target, cfg); err != nil {
		return err
	}
	return execEach(ctx, target, splitPostgres(sqlPostgres), cfg.Strict, "postgres/"+dbName)
}

// ensurePGSchema creates the fixture schema and makes it the default for this account.
// The fixture SQL never qualifies a name, so it lands wherever search_path points.
func ensurePGSchema(ctx context.Context, db *sql.DB, cfg Config) error {
	schema := pq.QuoteIdentifier(pgSchema(cfg))
	if _, err := db.ExecContext(ctx,
		"CREATE SCHEMA IF NOT EXISTS "+schema+" AUTHORIZATION CURRENT_USER"); err != nil {
		return fmt.Errorf("create schema %s: %w", pgSchema(cfg), err)
	}
	// Persist search_path for the account so a later psql session lands in the same
	// schema instead of an apparently empty database. A role may always set its own
	// defaults, so this works without superuser rights. Not fatal: the load itself
	// relies on the DSN, not on this.
	if _, err := db.ExecContext(ctx,
		"ALTER ROLE CURRENT_USER IN DATABASE "+pq.QuoteIdentifier(targetDB(cfg))+
			" SET search_path TO "+schema); err != nil {
		log.Printf("[warn] could not set the default search_path for this account: %v", err)
	}
	return nil
}

func checkPostgres(ctx context.Context, cfg Config) error {
	dbName := targetDB(cfg)
	db, err := sql.Open("postgres", pgDSN(cfg, dbName))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect postgres(%s) %s: %w", dbName, cfg.Host, err)
	}
	// Populated if a representative fixture table already exists in this DB. The
	// schema is quoted because it comes from .env, and to_regclass returns NULL
	// rather than erroring when the schema does not exist yet.
	qualified := pq.QuoteIdentifier(pgSchema(cfg)) + ".products"
	var reg sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)", qualified).Scan(&reg); err != nil {
		return fmt.Errorf("existence query: %w", err)
	}
	if reg.Valid {
		return fmt.Errorf("%w: table %s exists in %s on %s", ErrDataExists, qualified, dbName, cfg.Host)
	}
	return nil
}

func execEach(ctx context.Context, db *sql.DB, stmts []string, strict bool, label string) error {
	failed := 0
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			if strict {
				return fmt.Errorf("[%s] exec failed: %w\n--- statement ---\n%s", label, err, truncate(s, 200))
			}
			failed++
			log.Printf("[warn] %s stmt failed (continuing): %v | %s", label, err, truncate(s, 120))
		}
	}
	if failed > 0 {
		log.Printf("[info] %s finished with %d/%d statement warnings", label, failed, len(stmts))
	}
	return nil
}

// ---------------------------------------------------------------------------
// MongoDB (Extended JSON)
// ---------------------------------------------------------------------------

type mongoFixture struct {
	Database    string `json:"database"`
	Collections []struct {
		Name      string            `json:"name"`
		Documents []json.RawMessage `json:"documents"`
		Indexes   []struct {
			Keys   json.RawMessage `json:"keys"`
			Unique bool            `json:"unique"`
		} `json:"indexes"`
	} `json:"collections"`
}

// mongoURI builds the connection string for the single reachable endpoint.
//
// directConnection=true is what makes NCP's managed MongoDB reachable from outside.
// The instance is created as STAND_ALONE, but it still identifies itself as a replica
// set member, so the driver's default behaviour is to discover the topology and then
// talk to the hosts the server advertises - and those are private domains
// (49he6d.vpc.mg.naverncp.com -> 10.10.1.9). Connecting through the public domain then
// fails anyway:
//
//	server selection error: context deadline exceeded, current topology:
//	{ Type: ReplicaSetNoPrimary, Servers: [{ Addr: ...vpc.mg.naverncp.com:27017,
//	  Last error: dial tcp 10.10.1.9:27017: i/o timeout }] }
//
// directConnection pins the driver to the address it was given and skips discovery
// entirely. That is exactly right for a single-node target and harmless against any
// other MongoDB reached through a hand-assembled --inputs-file.
func mongoURI(cfg Config) string {
	return fmt.Sprintf("mongodb://%s:%s@%s:%s/?authSource=admin&directConnection=true",
		cfg.User, cfg.Password, cfg.Host, cfg.Port)
}

func mongoConnect(ctx context.Context, cfg Config) (*mongo.Client, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(cctx, options.Client().ApplyURI(mongoURI(cfg)))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(cctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("connect mongodb %s: %w", cfg.Host, err)
	}
	return client, nil
}

func loadMongo(ctx context.Context, cfg Config) error {
	var fx mongoFixture
	if err := json.Unmarshal(mongoJSON, &fx); err != nil {
		return fmt.Errorf("parse mongo fixture: %w", err)
	}
	client, err := mongoConnect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)

	db := client.Database(targetDB(cfg))
	for _, c := range fx.Collections {
		coll := db.Collection(c.Name)
		if err := coll.Drop(ctx); err != nil {
			return fmt.Errorf("drop %s: %w", c.Name, err)
		}
		docs := make([]interface{}, 0, len(c.Documents))
		for _, raw := range c.Documents {
			var d bson.D
			if err := bson.UnmarshalExtJSON(raw, false, &d); err != nil {
				return fmt.Errorf("decode doc in %s: %w", c.Name, err)
			}
			docs = append(docs, d)
		}
		if len(docs) > 0 {
			if _, err := coll.InsertMany(ctx, docs); err != nil {
				return fmt.Errorf("insert %s: %w", c.Name, err)
			}
		}
		for _, idx := range c.Indexes {
			var keys bson.D
			if err := bson.UnmarshalExtJSON(idx.Keys, false, &keys); err != nil {
				return fmt.Errorf("decode index keys in %s: %w", c.Name, err)
			}
			im := mongo.IndexModel{Keys: keys}
			if idx.Unique {
				im.Options = options.Index().SetUnique(true)
			}
			if _, err := coll.Indexes().CreateOne(ctx, im); err != nil {
				return fmt.Errorf("create index on %s: %w", c.Name, err)
			}
		}
	}
	return nil
}

// checkMongo reports ErrDataExists only when a collection the fixture itself owns
// is already present.
//
// "any collection at all" would be wrong: a MongoDB database is created lazily, so
// provisioning has to leave something behind for the database to exist. The AWS
// user_data creates a placeholder collection ("init") for exactly that reason, which
// made every first run on a freshly provisioned instance report existing data.
// This mirrors checkPostgres, which looks for a representative fixture table rather
// than for a non-empty schema.
func checkMongo(ctx context.Context, cfg Config) error {
	var fx mongoFixture
	if err := json.Unmarshal(mongoJSON, &fx); err != nil {
		return fmt.Errorf("parse mongo fixture: %w", err)
	}
	client, err := mongoConnect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)
	dbName := targetDB(cfg)
	names, err := client.Database(dbName).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("existence query: %w", err)
	}
	present := make(map[string]bool, len(names))
	for _, n := range names {
		present[n] = true
	}
	var found []string
	for _, c := range fx.Collections {
		if present[c.Name] {
			found = append(found, c.Name)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%w: %s on %s already has %s", ErrDataExists, dbName, cfg.Host, strings.Join(found, ", "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
