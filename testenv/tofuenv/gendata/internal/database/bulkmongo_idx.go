package database

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mongoIndexes is what one collection gives up for the load.
type mongoIndexes struct {
	coll    string
	indexes []mongoIndex
}

type mongoIndex struct {
	name string
	keys bson.D
}

// plainIndexFields are the only fields a plain index's specification carries.
// Anything else - unique, sparse, partialFilterExpression, expireAfterSeconds,
// collation - is an index this does not rebuild exactly, and it stays.
var plainIndexFields = map[string]bool{"v": true, "key": true, "name": true, "ns": true}

// dropMongoIndexes drops the plain secondary indexes of the collections being
// filled and returns a function that rebuilds them - the same bargain as on
// PostgreSQL: maintaining an index on a random field costs a random page per
// document once it outgrows the cache, and building it afterwards is one sorted
// pass. _id stays (its values are generated in order), and so do the unique
// indexes on email and sku.
//
// Best effort: an index that cannot be listed or dropped stays.
func dropMongoIndexes(ctx context.Context, client *mongo.Client, db *mongo.Database, colls []string) func() {
	var dropped []mongoIndexes
	var names []string
	for _, c := range colls {
		idx, err := plainMongoIndexes(ctx, db.Collection(c))
		if err != nil {
			log.Printf("[warn] could not list the indexes of %s (continuing, they stay in place): %v", c, err)
			continue
		}
		got := mongoIndexes{coll: c}
		for _, ix := range idx {
			if _, err := db.Collection(c).Indexes().DropOne(ctx, ix.name); err != nil {
				log.Printf("[warn] could not drop index %s.%s (continuing, the load will be slower): %v", c, ix.name, err)
				continue
			}
			got.indexes = append(got.indexes, ix)
			names = append(names, ix.name)
		}
		if len(got.indexes) > 0 {
			dropped = append(dropped, got)
		}
	}
	if len(dropped) == 0 {
		return func() {}
	}
	log.Printf("database: %d %s dropped for the bulk load, rebuilt once the test data is in (%s)",
		len(names), plural("index", len(names)), strings.Join(names, ", "))

	return func() {
		recreateMongoIndexes(ctx, client, db, dropped)
	}
}

// plainMongoIndexes lists the collection's indexes that dropMongoIndexes may
// take out of the way.
func plainMongoIndexes(ctx context.Context, coll *mongo.Collection) ([]mongoIndex, error) {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []mongoIndex
	for cur.Next(ctx) {
		var spec bson.D
		if err := cur.Decode(&spec); err != nil {
			return nil, err
		}
		var ix mongoIndex
		plain := true
		for _, e := range spec {
			if !plainIndexFields[e.Key] {
				plain = false
			}
			switch e.Key {
			case "name":
				ix.name, _ = e.Value.(string)
			case "key":
				ix.keys, _ = e.Value.(bson.D)
			}
		}
		if plain && ix.name != "" && ix.name != "_id_" && len(ix.keys) > 0 {
			out = append(out, ix)
		}
	}
	return out, cur.Err()
}

// recreateMongoIndexes rebuilds each collection's indexes in one createIndexes
// - one scan of the collection for all of them - reporting the build's progress
// from $currentOp as it goes.
func recreateMongoIndexes(ctx context.Context, client *mongo.Client, db *mongo.Database, dropped []mongoIndexes) {
	total := 0
	for _, d := range dropped {
		total += len(d.indexes)
	}
	log.Printf("database: rebuilding %d %s on %d %s",
		total, plural("index", total), len(dropped), plural("collection", len(dropped)))
	start := time.Now()
	failed := 0
	for i, d := range dropped {
		label := fmt.Sprintf("  [%d/%d] %s", i+1, len(dropped), d.coll)
		log.Printf("%s (%d %s) ...", label, len(d.indexes), plural("index", len(d.indexes)))
		models := make([]mongo.IndexModel, len(d.indexes))
		for j, ix := range d.indexes {
			models[j] = mongo.IndexModel{Keys: ix.keys, Options: options.Index().SetName(ix.name)}
		}
		began := time.Now()
		stop := newMongoIndexWatcher(ctx, client, db.Name(), d.coll).start(label)
		_, err := db.Collection(d.coll).Indexes().CreateMany(ctx, models)
		stop()
		if err != nil {
			failed += len(d.indexes)
			// Loud, because the database is now missing indexes the fixture
			// created and nothing later in the run would notice.
			var defs []string
			for _, ix := range d.indexes {
				defs = append(defs, fmt.Sprintf("%s %v", ix.name, ix.keys))
			}
			log.Printf("[ERROR] %s: indexes dropped for the bulk load could NOT be recreated: %v\n"+
				"  recreate them by hand, or reload the fixture: %s", d.coll, err, strings.Join(defs, "; "))
			continue
		}
		log.Printf("%s done in %s", label, time.Since(began).Round(time.Second))
	}
	log.Printf("database: %d %s rebuilt in %s",
		total-failed, plural("index", total-failed), time.Since(start).Round(time.Second))
}

// newMongoIndexWatcher reports an index build on coll from $currentOp. ownOps
// limits it to this user's operations, which needs no extra privilege.
func newMongoIndexWatcher(ctx context.Context, client *mongo.Client, dbName, coll string) *ddlWatcher {
	admin := client.Database("admin")
	return &ddlWatcher{ctx: ctx, poll: func(ctx context.Context) (string, bool) {
		cur, err := admin.Aggregate(ctx, mongo.Pipeline{
			{{Key: "$currentOp", Value: bson.D{{Key: "ownOps", Value: true}}}},
			{{Key: "$match", Value: bson.D{
				{Key: "ns", Value: dbName + "." + coll},
				{Key: "command.createIndexes", Value: coll},
			}}},
		})
		if err != nil {
			return "", false
		}
		defer cur.Close(ctx)
		if !cur.Next(ctx) {
			return "", false
		}
		var op struct {
			Msg      string `bson:"msg"`
			Progress struct {
				Done  float64 `bson:"done"`
				Total float64 `bson:"total"`
			} `bson:"progress"`
		}
		if err := cur.Decode(&op); err != nil {
			return "", false
		}
		return formatMongoIndexProgress(op.Msg, op.Progress.Done, op.Progress.Total), true
	}}
}

// formatMongoIndexProgress turns "Index Build: scanning collection Index Build:
// scanning collection: 1234/5678 21%" into "scanning collection 21% (1k/6k)".
func formatMongoIndexProgress(msg string, done, total float64) string {
	phase := strings.TrimPrefix(msg, "Index Build: ")
	if i := strings.Index(phase, ":"); i >= 0 {
		phase = phase[:i]
	}
	if j := strings.Index(phase, " Index Build"); j >= 0 {
		phase = phase[:j]
	}
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = "building"
	}
	if total > 0 {
		return fmt.Sprintf("%s %.0f%% (%s/%s)", phase, done*100/total, humanCount(int(done)), humanCount(int(total)))
	}
	return phase
}
