package storagex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
)

func TestLargestFirst(t *testing.T) {
	sizes := []int64{10, 50, 10, 0, 50, 30}
	got := largestFirst(len(sizes), func(i int) int64 { return sizes[i] })
	want := []int{1, 4, 5, 0, 2, 3} // ties keep listing order
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("largestFirst = %v, want %v", got, want)
		}
	}
}

func TestS3Parts(t *testing.T) {
	for _, tc := range []struct{ c, objects, parts int }{
		{0, 4, 4}, // unset: the default
		{1, 1, 1},
		{8, 8, 8},
		{16, 16, 16},
		{32, 32, 32}, // one object in parts at a time: 32 + 32
	} {
		ctx := core.WithConcurrency(context.Background(), tc.c)
		if o, p := s3Objects(ctx), s3Parts(ctx); o != tc.objects || p != tc.parts {
			t.Errorf("concurrency %d: objects, parts = %d, %d, want %d, %d", tc.c, o, p, tc.objects, tc.parts)
		}
		if s3Objects(ctx)+s3Parts(ctx) > s3MaxConnections {
			t.Errorf("concurrency %d: over s3MaxConnections", tc.c)
		}
	}
}

func TestReportS3Concurrency(t *testing.T) {
	for _, tc := range []struct {
		c        int
		wantNote bool
	}{{16, false}, {32, false}} { // up to MaxConcurrency, parts are never cut back
		var got core.Update
		ctx := core.WithReporter(core.WithConcurrency(context.Background(), tc.c),
			core.ReporterFunc(func(u core.Update) { got = u }))
		reportS3Concurrency(ctx)
		if got.Concurrency != tc.c {
			t.Errorf("concurrency %d: reported %d", tc.c, got.Concurrency)
		}
		if (got.ConcurrencyNote != "") != tc.wantNote {
			t.Errorf("concurrency %d: note %q", tc.c, got.ConcurrencyNote)
		}
	}
}

// TestForEachInLanes checks that objects in parts go one at a time, in order,
// while the rest run beside them, and that every index is called once.
func TestForEachInLanes(t *testing.T) {
	ctx := core.WithConcurrency(context.Background(), 4)
	order := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	inParts := func(i int) bool { return i < 3 } // 0, 1, 2 are large

	var (
		mu                 sync.Mutex
		called             = map[int]int{}
		partedOrder        []int
		partedNow, partMax int
		wholeDuringParts   int
	)
	err := forEachInLanes(ctx, order, inParts, func(ctx context.Context, i int) error {
		mu.Lock()
		called[i]++
		if inParts(i) {
			partedOrder = append(partedOrder, i)
			partedNow++
			partMax = max(partMax, partedNow)
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		if inParts(i) {
			partedNow--
		} else if partedNow > 0 {
			wholeDuringParts++
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range order {
		if called[i] != 1 {
			t.Errorf("index %d called %d times", i, called[i])
		}
	}
	if partMax != 1 {
		t.Errorf("objects in parts at once = %d, want 1", partMax)
	}
	if len(partedOrder) != 3 || partedOrder[0] != 0 || partedOrder[1] != 1 || partedOrder[2] != 2 {
		t.Errorf("objects in parts ran in order %v, want [0 1 2]", partedOrder)
	}
	if wholeDuringParts == 0 {
		t.Error("no small object ran beside an object in parts")
	}
}

// TestForEachInLanesFailure checks that a failure in one lane stops the other
// and is the error returned.
func TestForEachInLanesFailure(t *testing.T) {
	ctx := core.WithConcurrency(context.Background(), 2)
	boom := errors.New("boom")
	order := make([]int, 50)
	for i := range order {
		order[i] = i
	}
	var mu sync.Mutex
	ran := 0
	err := forEachInLanes(ctx, order, func(i int) bool { return i == 0 }, func(ctx context.Context, i int) error {
		if i == 0 {
			return boom
		}
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		ran++
		mu.Unlock()
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if ran >= 49 {
		t.Errorf("the other lane ran all %d of its objects after the failure", ran)
	}
}

// recordingStore serves presigned PUTs and GETs for a fake provider, keeping the
// order keys arrived in and the most requests it had in flight at once.
type recordingStore struct {
	srv     *httptest.Server
	objects []ObjectInfo
	delay   time.Duration

	mu       sync.Mutex
	order    []string
	inFlight int
	maxSeen  int
}

func newRecordingStore(t *testing.T, objects []ObjectInfo, delay time.Duration) *recordingStore {
	s := &recordingStore{objects: objects, delay: delay}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		s.order = append(s.order, key)
		s.inFlight++
		s.maxSeen = max(s.maxSeen, s.inFlight)
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.inFlight--; s.mu.Unlock() }()

		time.Sleep(s.delay)
		switch r.Method {
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
		case http.MethodGet:
			for _, o := range s.objects {
				if o.Key == key {
					_, _ = w.Write(make([]byte, o.Size))
				}
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *recordingStore) GeneratePresignedURL(_, key string) (PresignedURLResult, error) {
	return PresignedURLResult{URL: s.srv.URL + "/" + key}, nil
}
func (s *recordingStore) ListObjects(string) ([]ObjectInfo, error) { return s.objects, nil }
func (s *recordingStore) GetBucket() string                        { return "bucket" }

func writeFiles(t *testing.T, sizes map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	for name, size := range sizes {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestUploadLargestFirst(t *testing.T) {
	dir := writeFiles(t, map[string]int{"a.bin": 1 << 10, "b.bin": 50 << 10, "c.bin": 10 << 10})
	store := newRecordingStore(t, nil, 0)
	ctx := core.WithConcurrency(context.Background(), 1) // one at a time: the order is the schedule

	if err := (&S3Executor{Provider: store}).upload(ctx, dir, "bucket/prefix", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"prefix/b.bin", "prefix/c.bin", "prefix/a.bin"}
	if strings.Join(store.order, ",") != strings.Join(want, ",") {
		t.Errorf("PUT order = %v, want %v", store.order, want)
	}
}

func TestDownloadLargestFirst(t *testing.T) {
	store := newRecordingStore(t, []ObjectInfo{
		{Key: "p/small", Size: 10}, {Key: "p/large", Size: 5000}, {Key: "p/mid", Size: 500},
	}, 0)
	ctx := core.WithConcurrency(context.Background(), 1)

	if err := (&S3Executor{Provider: store}).download(ctx, "bucket/p", filepath.Join(t.TempDir(), "out"), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"p/large", "p/mid", "p/small"}
	if strings.Join(store.order, ",") != strings.Join(want, ",") {
		t.Errorf("GET order = %v, want %v", store.order, want)
	}
}

func TestUploadRunsConcurrencyAtOnce(t *testing.T) {
	sizes := map[string]int{}
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		sizes[n+".bin"] = 1 << 10
	}
	dir := writeFiles(t, sizes)
	store := newRecordingStore(t, nil, 30*time.Millisecond)
	ctx := core.WithConcurrency(context.Background(), 3)

	if err := (&S3Executor{Provider: store}).upload(ctx, dir, "bucket/x", nil); err != nil {
		t.Fatal(err)
	}
	if store.maxSeen != 3 {
		t.Errorf("most PUTs in flight = %d, want 3", store.maxSeen)
	}
}
