package database

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/brianvoe/gofakeit/v6"
)

// genCtx is the state one bulk run generates rows from.
//
// Everything in it comes from BulkOptions.Seed, so a run is reproducible: the
// same seed puts the same logical rows into MySQL, PostgreSQL and MongoDB alike,
// which is what makes "did the migration carry this row across" a comparison
// rather than a guess.
type genCtx struct {
	rng   *rand.Rand
	pools map[string]idPool

	// Pre-drawn strings. gofakeit costs a microsecond or so per call, which is
	// nothing per row and half an hour across ten million of them; drawing a few
	// hundred of each up front and picking from those keeps the generator well
	// clear of being the bottleneck, and the data no less varied than a test
	// fixture needs.
	firstNames []string
	lastNames  []string
	cities     []string
	streets    []string
	titles     []string
	texts      []string

	// now is fixed rather than time.Now(), so two runs with the same seed produce
	// identical timestamps as well as identical everything else.
	now time.Time
}

// samplePoolSize is how many of each string are drawn up front.
const samplePoolSize = 256

// idPool is where a generated row's foreign keys come from: either the id range
// this run is about to create, or - for a parent this run is not creating - a
// sample of what the fixture already put there.
type idPool struct {
	base, n int
	ids     []int
}

func (p idPool) pick(rng *rand.Rand) int {
	if p.n > 0 {
		return p.base + rng.Intn(p.n)
	}
	return p.ids[rng.Intn(len(p.ids))]
}

// parentOf lists, for each bulk table, the tables its rows point at.
var parentOf = map[string][]string{
	"products":      {"categories"},
	"orders":        {"customers"},
	"order_items":   {"orders", "products"},
	"reviews":       {"products", "customers"},
	"inventory_log": {"products"},
}

// parentPK names the primary key of each table a child may reference.
var parentPK = map[string]string{
	"categories": "category_id",
	"customers":  "customer_id",
	"products":   "product_id",
	"orders":     "order_id",
}

func newGenCtx(ctx context.Context, o BulkOptions, plan BulkPlan, refs refSource) (*genCtx, error) {
	fake := gofakeit.New(o.Seed)
	g := &genCtx{
		rng:   rand.New(rand.NewSource(o.Seed)),
		pools: map[string]idPool{},
		now:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	g.firstNames = drawStrings(fake.FirstName)
	g.lastNames = drawStrings(fake.LastName)
	g.cities = drawStrings(fake.City)
	g.streets = drawStrings(fake.Street)
	g.titles = drawStrings(fake.ProductName)
	g.texts = drawStrings(func() string { return fake.Sentence(25) })

	// Only the parents actually referenced by a planned table are resolved, so a
	// run that fills one table does not fail on an unrelated empty one.
	need := map[string]bool{}
	for table, rows := range plan.Rows {
		if rows == 0 {
			continue
		}
		for _, p := range parentOf[table] {
			need[p] = true
		}
	}
	// categories first, then in load order, so a message names the first gap.
	for _, name := range []string{"categories", "customers", "products", "orders"} {
		if !need[name] {
			continue
		}
		// A parent this run is creating is referenced by its own id range - no
		// round trip, and the children spread evenly over it.
		if n := plan.Rows[name]; n > 0 {
			g.pools[name] = idPool{base: o.IDOffset, n: n}
			continue
		}
		ids, err := refs.existingIDs(ctx, name, parentPK[name])
		if err != nil {
			return nil, fmt.Errorf("bulk: read existing %s ids: %w", name, err)
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("bulk: %s is empty, so there is nothing for the generated rows "+
				"to reference - load the shop_db fixture first, or give %s a weight", name, name)
		}
		g.pools[name] = idPool{ids: ids}
	}
	return g, nil
}

func drawStrings(fn func() string) []string {
	out := make([]string, samplePoolSize)
	for i := range out {
		out[i] = fn()
	}
	return out
}

func (g *genCtx) pick(ss []string) string { return ss[g.rng.Intn(len(ss))] }

func (g *genCtx) ref(table string) int { return g.pools[table].pick(g.rng) }

// when returns a timestamp inside the two years before g.now.
func (g *genCtx) when() time.Time {
	return g.now.Add(-time.Duration(g.rng.Intn(730*24)) * time.Hour)
}

// money rounds to the two decimals every amount column in shop_db stores.
func (g *genCtx) money(min, max float64) float64 {
	return math.Round((min+g.rng.Float64()*(max-min))*100) / 100
}

// ---------------------------------------------------------------------------
// row generators
//
// Each returns the values for its table's cols, in that order. The unique
// columns - email, sku - are derived from the row id rather than drawn, so they
// cannot collide however many rows are generated.
// ---------------------------------------------------------------------------

var (
	customerGrades = []string{"Bronze", "Silver", "Gold", "VIP"}
	orderStatuses  = []string{"pending", "confirmed", "shipped", "delivered", "cancelled", "refunded"}
	invChangeTypes = []string{"purchase", "sale", "adjustment", "return"}
)

func genCustomer(g *genCtx, id int) []any {
	return []any{
		id,
		fmt.Sprintf("bulk.%d@example.com", id),
		g.pick(g.firstNames),
		g.pick(g.lastNames),
		fmt.Sprintf("010-%04d-%04d", g.rng.Intn(10000), g.rng.Intn(10000)),
		g.pick(g.streets) + ", " + g.pick(g.cities),
		g.pick(g.cities),
		"KR",
		g.pick(customerGrades),
		g.rng.Intn(100) < 95,
		g.when(),
	}
}

func genProduct(g *genCtx, id int) []any {
	return []any{
		id,
		g.ref("categories"),
		fmt.Sprintf("%s %d", g.pick(g.titles), id),
		g.pick(g.texts),
		g.money(1000, 2_000_000),
		g.rng.Intn(10000),
		fmt.Sprintf("BULK-%09d", id),
		math.Round(g.rng.Float64()*30*1000) / 1000,
		g.rng.Intn(100) < 90,
		g.when(),
	}
}

func genOrder(g *genCtx, id int) []any {
	return []any{
		id,
		g.ref("customers"),
		g.pick(orderStatuses),
		g.money(5000, 3_000_000),
		g.pick(g.streets) + ", " + g.pick(g.cities),
		fmt.Sprintf("TRK%012d", id),
		g.pick(g.texts),
		g.when(),
	}
}

func genOrderItem(g *genCtx, id int) []any {
	return []any{
		id,
		g.ref("orders"),
		g.ref("products"),
		1 + g.rng.Intn(5),
		g.money(1000, 500_000),
		float64(g.rng.Intn(31)),
	}
}

func genReview(g *genCtx, id int) []any {
	return []any{
		id,
		g.ref("products"),
		g.ref("customers"),
		1 + g.rng.Intn(5), // chk_rating on PostgreSQL enforces 1..5
		g.pick(g.titles),
		g.pick(g.texts),
		g.rng.Intn(100) < 60,
		g.when(),
	}
}

func genInventoryLog(g *genCtx, id int) []any {
	after := g.rng.Intn(10000)
	return []any{
		id,
		g.ref("products"),
		g.pick(invChangeTypes),
		g.rng.Intn(100) - 50,
		after,
		g.rng.Intn(1_000_000),
		g.pick(g.texts),
		g.when(),
	}
}
