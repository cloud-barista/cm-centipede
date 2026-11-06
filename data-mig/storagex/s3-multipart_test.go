package storagex

// Integration tests below talk to real stores and skip unless pointed at one.
//
// MinIO (testenv/dockerenv: minio-target :39010, minio-source :39000):
//
//	export MINIO_TEST_ENDPOINT=172.24.78.163:39010
//	export MINIO_TEST_ACCESS_KEY=minioadmin
//	export MINIO_TEST_SECRET_KEY=<the testenv password>
//	export MINIO_TEST_BUCKET=backups
//	# for the object storage to object storage test, also:
//	export MINIO_TEST_SRC_ENDPOINT=172.24.78.163:39000
//	export MINIO_TEST_SRC_BUCKET=backups
//
// Azure, against Azurite started only for the run:
//
//	docker run --rm -d --name azurite-test -p 10000:10000 \
//	  mcr.microsoft.com/azure-storage/azurite azurite-blob --blobHost 0.0.0.0
//	export AZURITE_TEST_ENDPOINT=http://<docker host>:10000/devstoreaccount1
//	go test ./storagex/ -run Azurite -v
//	docker stop azurite-test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/minio/minio-go/v7"
)

// ============================================================================
// Fixtures
// ============================================================================

// fakeMultipart is a multipartUploader that keeps the parts in memory.
type fakeMultipart struct {
	part int64

	// failPart fails that part with failErr, failTimes times, before it lands.
	failPart  int
	failTimes int
	failErr   error
	// onPart, when set, runs as each part arrives.
	onPart func(n int)

	mu        sync.Mutex
	parts     map[int][]byte
	opened    map[int]int
	completed int
	aborted   bool
}

func (f *fakeMultipart) singlePutLimit() int64 { return 0 }
func (f *fakeMultipart) limitSource() string   { return "fake" }

func (f *fakeMultipart) startMultipart(context.Context, string, int64) (multipartUpload, error) {
	f.parts, f.opened = map[int][]byte{}, map[int]int{}
	return f, nil
}

func (f *fakeMultipart) partSize() int64 { return f.part }

func (f *fakeMultipart) uploadPart(ctx context.Context, n int, size int64, r io.Reader) error {
	if f.onPart != nil {
		f.onPart(n)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened[n]++
	if n == f.failPart && f.failTimes > 0 {
		f.failTimes--
		return f.failErr
	}
	if int64(len(data)) != size {
		return fmt.Errorf("part %d: got %d bytes, told %d", n, len(data), size)
	}
	f.parts[n] = data
	return nil
}

func (f *fakeMultipart) complete(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	return nil
}

func (f *fakeMultipart) abort(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = true
	return nil
}

// joined reassembles the parts in order.
func (f *fakeMultipart) joined() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []byte
	for n := 1; n <= len(f.parts); n++ {
		out = append(out, f.parts[n]...)
	}
	return out
}

// randomBytes returns n bytes that differ part to part, so a part written at
// the wrong offset shows.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

// lowerThreshold sets multipartThreshold for the length of the test.
func lowerThreshold(t *testing.T, n int64) {
	t.Helper()
	old := multipartThreshold
	multipartThreshold = n
	t.Cleanup(func() { multipartThreshold = old })
}

// ============================================================================
// runMultipart
// ============================================================================

func TestRunMultipartSplitsIntoParts(t *testing.T) {
	for _, size := range []int{10, 30, 31, 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := randomBytes(size)
			f := &fakeMultipart{part: 10}
			var last int64
			err := runMultipart(context.Background(), f, "k", int64(size), sectionOpener(bytes.NewReader(data)),
				func(w int64) { atomic.StoreInt64(&last, max(atomic.LoadInt64(&last), w)) })
			if err != nil {
				t.Fatal(err)
			}
			if want := (size + 9) / 10; len(f.parts) != want {
				t.Errorf("%d parts, want %d", len(f.parts), want)
			}
			if !bytes.Equal(f.joined(), data) {
				t.Error("parts do not reassemble into the source")
			}
			if f.completed != 1 || f.aborted {
				t.Errorf("completed %d, aborted %v; want completed once", f.completed, f.aborted)
			}
			if last != int64(size) {
				t.Errorf("written reported up to %d, want %d", last, size)
			}
		})
	}
}

// A failed part is sent again, read from its start, and the rest are not.
func TestRunMultipartRetriesOnlyTheFailedPart(t *testing.T) {
	data := randomBytes(30)
	f := &fakeMultipart{part: 10, failPart: 2, failTimes: 1, failErr: errors.New("connection reset")}
	if err := runMultipart(context.Background(), f, "k", 30, sectionOpener(bytes.NewReader(data)), nil); err != nil {
		t.Fatal(err)
	}
	if f.opened[1] != 1 || f.opened[2] != 2 || f.opened[3] != 1 {
		t.Errorf("attempts per part = %v, want 1, 2, 1", f.opened)
	}
	if !bytes.Equal(f.joined(), data) {
		t.Error("the retried part did not reassemble into the source")
	}
}

func TestRunMultipartAbortsOnFailure(t *testing.T) {
	f := &fakeMultipart{part: 10, failPart: 2, failTimes: 99, failErr: permanent(errors.New("403 forbidden"))}
	err := runMultipart(context.Background(), f, "k", 30, sectionOpener(bytes.NewReader(randomBytes(30))), nil)
	if err == nil || !strings.Contains(err.Error(), "part 2 of 3") {
		t.Fatalf("err = %v, want part 2's failure", err)
	}
	if !f.aborted || f.completed != 0 {
		t.Errorf("aborted %v, completed %d; want aborted, never completed", f.aborted, f.completed)
	}
	if f.opened[2] != 1 {
		t.Errorf("a permanent failure was tried %d times", f.opened[2])
	}
}

func TestRunMultipartAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeMultipart{part: 10, onPart: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	err := runMultipart(ctx, f, "k", 50, sectionOpener(bytes.NewReader(randomBytes(50))), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !f.aborted || f.completed != 0 {
		t.Errorf("aborted %v, completed %d; want a cancelled upload aborted", f.aborted, f.completed)
	}
}

// A provider that writes in parts takes any size, so a preflight is not set
// for it even though it reports a singlePutLimit.
func TestPreflightSkipsMultipartProvider(t *testing.T) {
	type both struct {
		*fakeMultipart
		fakeLister
	}
	dst := both{fakeMultipart: &fakeMultipart{}}
	if pf := singlePutPreflight(dst, "file", nil); pf != nil {
		t.Error("a multipart provider got a single-PUT preflight")
	}
}

// ============================================================================
// Azure block layout
// ============================================================================

func TestAzureBlockSize(t *testing.T) {
	p, err := NewAzureProvider(&AzureConfig{Endpoint: "acct.blob.core.windows.net", AccountKey: testAccountKey}, "c")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		size, block int64
	}{
		{6 << 30, 64 << 20},            // the floor
		{4 << 40, 84 << 20},            // 4 TiB / 50,000 = 83.9 MiB, rounded up to whole MiB
		{50000 * (64 << 20), 64 << 20}, // exactly 50,000 floor blocks
	}
	for _, c := range cases {
		up, err := p.startMultipart(context.Background(), "k", c.size)
		if err != nil {
			t.Fatalf("size %d: %v", c.size, err)
		}
		if got := up.partSize(); got != c.block {
			t.Errorf("size %d: block %d, want %d", c.size, got, c.block)
		}
	}
	if _, err := p.startMultipart(context.Background(), "k", 50000*4000<<20+1); err == nil {
		t.Error("an object beyond 50,000 blocks of 4000 MiB was accepted")
	}
}

func TestAzureBlockIDsShareALength(t *testing.T) {
	if a, b := azureBlockID(1), azureBlockID(50000); len(a) != len(b) {
		t.Errorf("block IDs %q and %q differ in length", a, b)
	}
}

// ============================================================================
// MinIO (integration)
// ============================================================================

// minioTestProvider connects to the store named by MINIO_TEST_<prefix>ENDPOINT,
// or skips.
func minioTestProvider(t *testing.T, prefix string) *MinioProvider {
	t.Helper()
	endpoint := os.Getenv("MINIO_TEST_" + prefix + "ENDPOINT")
	if endpoint == "" {
		t.Skipf("MINIO_TEST_%sENDPOINT not set; skipping MinIO integration test", prefix)
	}
	bucket := os.Getenv("MINIO_TEST_" + prefix + "BUCKET")
	if bucket == "" {
		bucket = os.Getenv("MINIO_TEST_BUCKET")
	}
	p, err := NewMinioProvider(&MinioConfig{
		Endpoint:        endpoint,
		AccessKeyId:     os.Getenv("MINIO_TEST_ACCESS_KEY"),
		SecretAccessKey: os.Getenv("MINIO_TEST_SECRET_KEY"),
	}, bucket)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// minioCleanup removes every object under prefix when the test ends.
func minioCleanup(t *testing.T, p *MinioProvider, prefix string) {
	t.Cleanup(func() {
		ctx := context.Background()
		for obj := range p.client.ListObjects(ctx, p.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			_ = p.client.RemoveObject(ctx, p.bucket, obj.Key, minio.RemoveObjectOptions{})
		}
	})
}

// minioContent returns the stored object's size, ETag and SHA-256.
func minioContent(t *testing.T, p *MinioProvider, key string) (int64, string, [32]byte) {
	t.Helper()
	ctx := context.Background()
	info, err := p.client.StatObject(ctx, p.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		t.Fatalf("stat %s: %v", key, err)
	}
	obj, err := p.client.GetObject(ctx, p.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		t.Fatal(err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return info.Size, info.ETag, sum
}

// Above the threshold a file goes up in parts — minio-go's 16 MiB ones — and
// lands whole; at it, a file keeps its single PUT and its MD5 ETag.
func TestMinioUploadSwitchesToMultipart(t *testing.T) {
	p := minioTestProvider(t, "")
	lowerThreshold(t, 8<<20)
	prefix := fmt.Sprintf("datamig-multipart-test/%d", time.Now().UnixNano())
	minioCleanup(t, p, prefix)

	dir := t.TempDir()
	files := map[string][]byte{
		"big.bin":   randomBytes(40 << 20), // parts of 16, 16 and 8 MiB
		"exact.bin": randomBytes(8 << 20),  // the threshold itself
		"empty.bin": {},
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	e := NewS3Executor(p)
	if err := e.Execute(context.Background(), localLoc(dir), s3Loc(p.bucket+"/"+prefix)); err != nil {
		t.Fatal(err)
	}

	for name, data := range files {
		size, etag, sum := minioContent(t, p, prefix+"/"+name)
		if size != int64(len(data)) || sum != sha256.Sum256(data) {
			t.Errorf("%s: landed %d bytes with a different content, want %d", name, size, len(data))
		}
		multipart := strings.Contains(etag, "-")
		if want := name == "big.bin"; multipart != want {
			t.Errorf("%s: ETag %q, multipart %v, want %v", name, etag, multipart, want)
		}
		if name == "big.bin" && !strings.HasSuffix(etag, "-3") {
			t.Errorf("big.bin: ETag %q, want 3 parts", etag)
		}
	}
}

// Object storage to object storage: a large object streams across in ranged
// parts and lands identical.
func TestMinioStreamSwitchesToMultipart(t *testing.T) {
	src := minioTestProvider(t, "SRC_")
	dst := minioTestProvider(t, "")
	lowerThreshold(t, 8<<20)
	prefix := fmt.Sprintf("datamig-multipart-test/%d", time.Now().UnixNano())
	minioCleanup(t, src, prefix)
	minioCleanup(t, dst, prefix)

	data := randomBytes(40<<20 + 12345)
	ctx := context.Background()
	if _, err := src.client.PutObject(ctx, src.bucket, prefix+"/in/big.bin", bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}

	e := NewS3StreamExecutor(src, dst)
	if err := e.Execute(ctx, s3Loc(src.bucket+"/"+prefix+"/in"), s3Loc(dst.bucket+"/"+prefix+"/out")); err != nil {
		t.Fatal(err)
	}

	size, etag, sum := minioContent(t, dst, prefix+"/out/big.bin")
	if size != int64(len(data)) || sum != sha256.Sum256(data) {
		t.Errorf("landed %d bytes with a different content, want %d", size, len(data))
	}
	if !strings.HasSuffix(etag, "-3") {
		t.Errorf("ETag %q, want 3 parts", etag)
	}
}

// ============================================================================
// Azure (integration, Azurite)
// ============================================================================

// azuriteAccountKey is the well-known key of Azurite's development account,
// published with the emulator; it guards nothing.
const azuriteAccountKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

// azuriteProvider connects to the Azurite named by AZURITE_TEST_ENDPOINT, in a
// container of its own, or skips. NewAzureProvider insists on an Azure host
// over HTTPS, so the provider is assembled here around an emulator client.
func azuriteProvider(t *testing.T) *AzureProvider {
	t.Helper()
	endpoint := os.Getenv("AZURITE_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("AZURITE_TEST_ENDPOINT not set; skipping Azurite integration test")
	}
	account := "devstoreaccount1"
	cred, err := azblob.NewSharedKeyCredential(account, azuriteAccountKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(strings.TrimSuffix(endpoint, "/")+"/", cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	container := fmt.Sprintf("datamig-multipart-%d", time.Now().UnixNano())
	if _, err := client.CreateContainer(context.Background(), container, nil); err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() { _, _ = client.DeleteContainer(context.Background(), container, nil) })
	return &AzureProvider{client: client, cred: cred, container: container, account: account}
}

// azuriteContent returns the stored blob's bytes.
func azuriteContent(t *testing.T, p *AzureProvider, key string) []byte {
	t.Helper()
	resp, err := p.client.DownloadStream(context.Background(), p.container, key, nil)
	if err != nil {
		t.Fatalf("download %s: %v", key, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A file above the threshold goes up as staged blocks and commits whole.
func TestAzuriteUploadStagesBlocks(t *testing.T) {
	p := azuriteProvider(t)
	lowerThreshold(t, 1<<20)
	old := azureMinBlockSize
	azureMinBlockSize = 8 << 20
	t.Cleanup(func() { azureMinBlockSize = old })

	dir := t.TempDir()
	data := randomBytes(20<<20 + 7) // blocks of 8, 8 and 4 MiB + 7 B
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	e := NewS3Executor(p)
	if err := e.Execute(context.Background(), localLoc(filepath.Join(dir, "big.bin")), s3Loc(p.container+"/up")); err != nil {
		t.Fatal(err)
	}
	if got := azuriteContent(t, p, "up/big.bin"); !bytes.Equal(got, data) {
		t.Errorf("landed %d bytes with a different content, want %d", len(got), len(data))
	}

	blocks, err := p.client.ServiceClient().NewContainerClient(p.container).NewBlockBlobClient("up/big.bin").GetBlockList(context.Background(), "committed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(blocks.CommittedBlocks); n != 3 {
		t.Errorf("%d committed blocks, want 3", n)
	}
}

// A stream source cannot seek, so each block is held in memory before it is
// staged; the blob still lands whole.
func TestAzuriteStagesStreamedBlocks(t *testing.T) {
	p := azuriteProvider(t)
	old := azureMinBlockSize
	azureMinBlockSize = 8 << 20
	t.Cleanup(func() { azureMinBlockSize = old })

	data := randomBytes(17 << 20)
	open := func(off, n int64) (io.ReadCloser, error) {
		// Hides Seek, as an HTTP response body would.
		return io.NopCloser(struct{ io.Reader }{bytes.NewReader(data[off : off+n])}), nil
	}
	if err := runMultipart(context.Background(), p, "stream/big.bin", int64(len(data)), open, nil); err != nil {
		t.Fatal(err)
	}
	if got := azuriteContent(t, p, "stream/big.bin"); !bytes.Equal(got, data) {
		t.Errorf("landed %d bytes with a different content, want %d", len(got), len(data))
	}
}
