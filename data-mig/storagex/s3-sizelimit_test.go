package storagex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ============================================================================
// Fixtures
// ============================================================================

// fakeLimiter is a singlePutLimiter with a fixed limit.
type fakeLimiter struct {
	limit  int64
	source string
}

func (f fakeLimiter) singlePutLimit() int64 { return f.limit }
func (f fakeLimiter) limitSource() string   { return f.source }

// tumblebugLoc builds a cb-tumblebug object storage location served by endpoint.
func tumblebugLoc(endpoint, path string) DataLocation {
	return DataLocation{
		StorageType: StorageTypeObjectStorage,
		Path:        path,
		ObjectStorage: &ObjectStorageAccess{
			AccessType: AccessTypeTumblebug,
			Tumblebug:  &TumblebugConfig{Endpoint: endpoint, NsId: "ns01", OsId: "os01"},
		},
	}
}

// tumblebugServer answers cb-tumblebug's object storage listing with provider as
// the cloud behind os01, and counts the listings and the presigned URL requests
// it was asked for.
func tumblebugServer(t *testing.T, provider string) (srv *httptest.Server, listings, presigns *atomic.Int32) {
	t.Helper()
	listings, presigns = new(atomic.Int32), new(atomic.Int32)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ns/ns01/resources/objectStorage":
			listings.Add(1)
			if r.URL.Query().Get("filterKey") != "id" || r.URL.Query().Get("filterVal") != "os01" {
				t.Errorf("listing query = %q, want filterKey=id&filterVal=os01", r.URL.RawQuery)
			}
			// A substring filter lets a neighbour through; it must not be taken.
			fmt.Fprintf(w, `{"objectStorage":[
				{"id":"os01-old","connectionConfig":{"providerName":"gcp"}},
				{"id":"os01","connectionConfig":{"providerName":%q}}]}`, provider)
		case strings.HasSuffix(r.URL.Path, "/presignedUrl"):
			presigns.Add(1)
			http.Error(w, "no presigned URL in this test", http.StatusTeapot)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, listings, presigns
}

// writeSized creates a file of size bytes under dir. Truncate leaves it sparse,
// so a file of gigabytes costs no disk.
func writeSized(t *testing.T, dir, name string, size int64) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// lowerLimit sets presignedPutLimit for the length of the test.
func lowerLimit(t *testing.T, n int64) {
	t.Helper()
	old := presignedPutLimit
	presignedPutLimit = n
	t.Cleanup(func() { presignedPutLimit = old })
}

// ============================================================================
// Limits
// ============================================================================

func TestPutLimitFor(t *testing.T) {
	cases := []struct {
		csp  string
		want int64
	}{
		{"aws", 5 << 30},
		{"AWS", 5 << 30}, // cb-spider's spelling
		{"azure", 5000 << 20},
		{"AZURE", 5000 << 20},
		{"gcp", 5 << 40},
		{"NCP", 10_000_000_000_000},
		{"alibaba", 5 << 30},
		{"kt", 5 << 30},
		{"", 5 << 30},
	}
	for _, c := range cases {
		if got := putLimitFor(c.csp); got != c.want {
			t.Errorf("putLimitFor(%q) = %d, want %d", c.csp, got, c.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		512:             "512 B",
		5 << 30:         "5 GiB",
		5000 << 20:      "5000 MiB",
		5<<30 + 1:       "5.0 GiB",
		6_657_199_308:   "6.2 GiB",
		5 << 40:         "5 TiB",
		1536:            "1.5 KiB",
		(5 << 30) + 512: "5.0 GiB",
	}
	for n, want := range cases {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// ============================================================================
// checkSinglePutLimit
// ============================================================================

func TestCheckSinglePutLimitBoundary(t *testing.T) {
	lim := fakeLimiter{limit: 1 << 20, source: "aws behind tumblebug"}

	if err := checkSinglePutLimit([]sizedItem{{"a", 1 << 20}, {"b", 0}}, "file", lim); err != nil {
		t.Fatalf("an item of exactly the limit must fit: %v", err)
	}

	err := checkSinglePutLimit([]sizedItem{{"a", 1 << 20}, {"big.iso", 1<<20 + 1}}, "file", lim)
	if err == nil {
		t.Fatal("an item one byte over the limit must be refused")
	}
	var perm *permanentError
	if !errors.As(err, &perm) {
		t.Errorf("error is not permanent: %v", err)
	}
	for _, want := range []string{"1 file(s)", "1 MiB single-PUT limit of aws behind tumblebug", "big.iso (1.0 MiB)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "a (") {
		t.Errorf("error names an item that fits: %v", err)
	}
}

func TestCheckSinglePutLimitNamesTenAtMost(t *testing.T) {
	var items []sizedItem
	for i := range 13 {
		items = append(items, sizedItem{Name: fmt.Sprintf("obj%02d", i), Size: 2})
	}
	err := checkSinglePutLimit(items, "object", fakeLimiter{limit: 1, source: "spider (cloud provider unknown)"})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "13 object(s)") || !strings.Contains(msg, ", and 3 more") {
		t.Errorf("error %q should count 13 and name 10", msg)
	}
	if strings.Contains(msg, "obj10") {
		t.Errorf("error names an 11th item: %q", msg)
	}
}

// ============================================================================
// Source listings
// ============================================================================

// The files a preflight weighs are the ones the transfer sends: an excluded
// file, or one under an excluded directory, is not held against the limit.
func TestFsSizesFollowsFilter(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, dir, "keep.bin", 10)
	writeSized(t, dir, "skip.log", 99)
	writeSized(t, dir, "cache/inside.bin", 99)

	src := localLoc(dir)
	src.Filter = &FilterOption{Exclude: []string{"*.log", "cache"}}

	items, err := fsSizes(src)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "keep.bin" || items[0].Size != 10 {
		t.Errorf("items = %+v, want only keep.bin (10)", items)
	}
}

// A source that is one file is named by its own name, and sent unfiltered.
func TestFsSizesSingleFileSource(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, dir, "dump.sql", 42)

	src := localLoc(filepath.Join(dir, "dump.sql"))
	src.Filter = &FilterOption{Exclude: []string{"*"}}

	items, err := fsSizes(src)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "dump.sql" || items[0].Size != 42 {
		t.Errorf("items = %+v, want dump.sql (42)", items)
	}
}

// fakeLister is an S3Provider that only lists.
type fakeLister struct{ objects []ObjectInfo }

func (f fakeLister) GeneratePresignedURL(string, string) (PresignedURLResult, error) {
	return PresignedURLResult{}, errors.New("not in this test")
}
func (f fakeLister) ListObjects(string) ([]ObjectInfo, error) { return f.objects, nil }
func (f fakeLister) GetBucket() string                        { return "src" }

// Objects are selected as the stream executor selects them: under the prefix as
// under a folder, and through the filter.
func TestObjectSizesSelectsLikeTheTransfer(t *testing.T) {
	src := s3Loc("src/data")
	src.Filter = &FilterOption{Exclude: []string{"*.tmp"}}
	p := fakeLister{objects: []ObjectInfo{
		{Key: "data/a.bin", Size: 1},
		{Key: "data/b.tmp", Size: 2},
		{Key: "datax/c.bin", Size: 3}, // a string prefix match, not under data/
	}}

	items, err := objectSizes(src, p)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "data/a.bin" {
		t.Errorf("items = %+v, want only data/a.bin", items)
	}
}

// ============================================================================
// Pipeline.Preflight
// ============================================================================

// countingExecutor counts its runs.
type countingExecutor struct{ runs int }

func (e *countingExecutor) Execute(context.Context, DataLocation, DataLocation) error {
	e.runs++
	return nil
}

func TestPipelinePreflightFailureRunsNoStep(t *testing.T) {
	exec := &countingExecutor{}
	cleaned := false
	p := &Pipeline{
		Steps:     []Step{{Name: "one", Executor: exec}, {Name: "two", Executor: exec}},
		Cleanup:   func() { cleaned = true },
		Preflight: func(context.Context) error { return errors.New("too big") },
	}

	err := p.Execute(context.Background())
	if err == nil || !strings.Contains(err.Error(), "preflight failed: too big") {
		t.Fatalf("err = %v, want the preflight's", err)
	}
	if exec.runs != 0 {
		t.Errorf("%d step(s) ran after a failed preflight", exec.runs)
	}
	if !cleaned {
		t.Error("Cleanup did not run after a failed preflight")
	}
}

func TestPipelineWithoutPreflightRunsSteps(t *testing.T) {
	exec := &countingExecutor{}
	p := &Pipeline{Steps: []Step{{Name: "one", Executor: exec}, {Name: "two", Executor: exec}}}
	if err := p.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exec.runs != 2 {
		t.Errorf("runs = %d, want 2", exec.runs)
	}
}

// ============================================================================
// Provider limits
// ============================================================================

func TestTumblebugLimitFromItsCloud(t *testing.T) {
	srv, listings, _ := tumblebugServer(t, "azure")
	p, err := NewTumblebugProvider(&TumblebugConfig{Endpoint: srv.URL, NsId: "ns01", OsId: "os01"})
	if err != nil {
		t.Fatal(err)
	}

	if got := p.singlePutLimit(); got != 5000<<20 {
		t.Errorf("limit = %d, want 5000 MiB", got)
	}
	if got := p.limitSource(); got != "azure behind tumblebug" {
		t.Errorf("source = %q", got)
	}
	p.singlePutLimit()
	if n := listings.Load(); n != 1 {
		t.Errorf("cb-tumblebug was asked %d times, want once", n)
	}
}

func TestTumblebugLimitWhenCloudUnknown(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"error status": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusInternalServerError) },
		"bad json":     func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "{") },
		"not listed":   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"objectStorage":[]}`) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			p, err := NewTumblebugProvider(&TumblebugConfig{Endpoint: srv.URL, NsId: "ns01", OsId: "os01"})
			if err != nil {
				t.Fatal(err)
			}
			if got := p.singlePutLimit(); got != presignedPutLimit {
				t.Errorf("limit = %d, want the default %d", got, presignedPutLimit)
			}
			if got := p.limitSource(); got != "tumblebug (cloud provider unknown)" {
				t.Errorf("source = %q", got)
			}
		})
	}
}

func TestSpiderLimitFromItsCloud(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path != "/connectionconfig/azure-conn" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"ConfigName":"azure-conn","ProviderName":"AZURE","RegionName":"koreacentral"}`)
	}))
	defer srv.Close()

	p, err := NewSpiderProvider(&SpiderConfig{Endpoint: srv.URL, ConnectionName: "azure-conn"}, "bucket")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.singlePutLimit(); got != 5000<<20 {
		t.Errorf("limit = %d, want 5000 MiB", got)
	}
	if got := p.limitSource(); got != "azure behind spider" {
		t.Errorf("source = %q", got)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("cb-spider was asked %d times, want once", n)
	}
}

func TestSpiderLimitWhenCloudUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, err := NewSpiderProvider(&SpiderConfig{Endpoint: srv.URL, ConnectionName: "c"}, "bucket")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.singlePutLimit(); got != presignedPutLimit {
		t.Errorf("limit = %d, want the default", got)
	}
	if got := p.limitSource(); got != "spider (cloud provider unknown)" {
		t.Errorf("source = %q", got)
	}
}

// ============================================================================
// Planner wiring
// ============================================================================

// Only a destination with a single-PUT limit gets a preflight; one that has
// none plans exactly as before.
func TestPlannerSetsPreflightForLimitedDestination(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		dmm  DataMigrationModel
		want bool
	}{
		{"local to tumblebug", DataMigrationModel{Source: localLoc(dir), Destination: tumblebugLoc("http://198.51.100.5", "os01/x")}, true},
		{"minio to tumblebug", DataMigrationModel{Source: s3Loc("src/x"), Destination: tumblebugLoc("http://198.51.100.5", "os01/x")}, true},
		{"minio to tumblebug relay", DataMigrationModel{Source: s3Loc("src/x"), Destination: tumblebugLoc("http://198.51.100.5", "os01/x"), Strategy: StrategyRelay}, true},
		{"local to minio", DataMigrationModel{Source: localLoc(dir), Destination: s3Loc("dst/x")}, false},
		{"minio to minio", DataMigrationModel{Source: s3Loc("src/x"), Destination: s3Loc("dst/x")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Plan(c.dmm)
			if err != nil {
				t.Fatal(err)
			}
			if p.Cleanup != nil {
				defer p.Cleanup()
			}
			if got := p.Preflight != nil; got != c.want {
				t.Errorf("Preflight set = %v, want %v", got, c.want)
			}
		})
	}
}

// End to end through the planner: a file one byte over AWS's 5 GiB, behind
// cb-tumblebug, fails the transfer before a single presigned URL is asked for.
func TestUploadOverLimitFailsBeforeSending(t *testing.T) {
	srv, _, presigns := tumblebugServer(t, "aws")
	dir := t.TempDir()
	writeSized(t, dir, "small.bin", 1024)
	writeSized(t, dir, "images/disk.img", 5<<30+1)

	p, err := Plan(DataMigrationModel{Source: localLoc(dir), Destination: tumblebugLoc(srv.URL, "os01/backup")})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Execute(context.Background())
	if err == nil {
		t.Fatal("want the transfer refused")
	}
	for _, want := range []string{"preflight failed", "1 file(s)", "5 GiB single-PUT limit of aws behind tumblebug", "images/disk.img (5.0 GiB)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if n := presigns.Load(); n != 0 {
		t.Errorf("%d presigned URL(s) were requested before the refusal", n)
	}
}

// The same source under a cloud that takes it in one PUT is let through to the
// transfer, which is where this test's server stops it.
func TestUploadUnderCloudLimitReachesTransfer(t *testing.T) {
	lowerLimit(t, 1<<20) // so a 2 MiB file is over the default but under gcp's
	srv, _, presigns := tumblebugServer(t, "gcp")
	dir := t.TempDir()
	writeSized(t, dir, "two.bin", 2<<20)

	p, err := Plan(DataMigrationModel{Source: localLoc(dir), Destination: tumblebugLoc(srv.URL, "os01/backup")})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Execute(context.Background())
	if err == nil || strings.Contains(err.Error(), "preflight") {
		t.Fatalf("err = %v, want the transfer's own failure, not the preflight's", err)
	}
	if presigns.Load() == 0 {
		t.Error("the transfer never asked for a presigned URL")
	}
}
