package storagex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/filter"
	"golang.org/x/sync/errgroup"
)

// S3Executor implements Executor for S3 object storage transfers.
// Uses presigned URLs for authentication-free upload/download.
type S3Executor struct {
	Provider S3Provider // S3 provider for generating presigned URLs
}

// NewS3Executor creates a new S3Executor with the given provider.
func NewS3Executor(provider S3Provider) *S3Executor {
	return &S3Executor{
		Provider: provider,
	}
}

// Execute performs S3 transfer from source to destination.
func (e *S3Executor) Execute(ctx context.Context, source, destination DataLocation) error {
	// Determine transfer direction based on StorageType
	srcIsS3 := source.IsObjectStorage()
	dstIsS3 := destination.IsObjectStorage()

	switch {
	case !srcIsS3 && dstIsS3:
		// Filesystem -> S3 (upload)
		return e.upload(ctx, source.Path, destination.Path, source.Filter)
	case srcIsS3 && !dstIsS3:
		// S3 -> Filesystem (download)
		return e.download(ctx, source.Path, destination.Path, source.Filter)
	case srcIsS3 && dstIsS3:
		// S3 -> S3 (not implemented yet)
		return fmt.Errorf("S3 to S3 transfer not implemented")
	default:
		return fmt.Errorf("invalid transfer: both source and destination are filesystem")
	}
}

// s3Concurrency is how many objects move at once in each direction.
//
// One at a time, a transfer of many small objects is bound by round trips, not
// bandwidth: each object costs a presigned URL — through Tumblebug or Spider an
// API call of its own — and a request, and the next waits for both. Several in
// flight hide those round trips behind one another.
const s3Concurrency = 8

// s3Attempts is how many times one object is tried before the transfer fails
// on it. A transient error — a dropped connection, a 5xx — otherwise ended a
// transfer of thousands of objects on the one it hit.
const s3Attempts = 3

// upload transfers local files to S3.
func (e *S3Executor) upload(ctx context.Context, localPath, s3Path string, flt *FilterOption) error {
	files, err := e.listLocalFiles(localPath, flt)
	if err != nil {
		return Obj{Kind: ObjectKindFile, Owner: localPath}.Fail(ActionList, err)
	}

	// Parse bucket and key from s3Path (e.g., "bucket-name/prefix/")
	_, keyPrefix := ParseBucketAndKey(s3Path)

	basePath := localPath
	if !isDirectory(localPath) {
		basePath = filepath.Dir(localPath)
	}

	progress := &s3Progress{reporter: core.ReporterFrom(ctx)}

	return forEachConcurrently(ctx, len(files), func(ctx context.Context, i int) error {
		file := files[i]

		// A transfer of thousands of files reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindFile, Name: file, Index: i + 1, Total: len(files)}

		relPath, err := filepath.Rel(basePath, file)
		if err != nil {
			return item.Fail(ActionUpload, err)
		}

		// Use only the key prefix, not the full path including bucket name
		s3Key := filepath.Join(keyPrefix, relPath)
		s3Key = strings.ReplaceAll(s3Key, "\\", "/") // Normalize to forward slashes

		if err := withRetry(ctx, func() error { return e.uploadFile(ctx, file, s3Key) }); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return item.Fail(ActionUpload, err)
		}

		// After the upload, not before: what is reported is what landed.
		var size int64
		if info, statErr := os.Stat(file); statErr == nil {
			size = info.Size()
		}
		progress.done(size, s3Key)
		return nil
	})
}

// download transfers S3 objects to local filesystem.
func (e *S3Executor) download(ctx context.Context, s3Path, localPath string, flt *FilterOption) error {
	// Parse bucket and key from s3Path (e.g., "bucket-name/prefix/")
	_, keyPrefix := ParseBucketAndKey(s3Path)

	objects, err := e.Provider.ListObjects(keyPrefix)
	if err != nil {
		return Obj{Kind: ObjectKindObject, Owner: keyPrefix}.Fail(ActionList, err)
	}

	// The filter is applied up front so only matching objects take a worker;
	// each keeps its index in the full listing for the error that names it.
	var matched []int
	for i, obj := range objects {
		if flt.MatchPath(filter.Rel(keyPrefix, obj.Key), obj.Size, false) {
			matched = append(matched, i)
		}
	}

	// Every key is placed before anything is written, and the destination is
	// cleared only once they all can be: a key that cannot become a local path
	// fails the transfer here, not after the destination is gone and part of
	// the bucket is down (see objectLocalPaths).
	keys := make([]string, len(matched))
	for n, i := range matched {
		keys[n] = objects[i].Key
	}
	places, skip, err := objectLocalPaths(localPath, keyPrefix, keys)
	if err != nil {
		return err
	}

	// Empty the destination before downloading, so the download replaces what is
	// there rather than merging into it.
	//
	// On a relay this is belt and braces: newStagingLocation hands every transfer
	// a directory of its own, so there is nothing to clear. It used to be the
	// thing that kept one relay's leftovers out of the next one's upload, and the
	// comment here said so — that job now belongs to the staging directory being
	// unique.
	//
	// ⚠ localPath is NOT always staging. On a direct objectstorage → filesystem
	//   transfer it is the caller's own destination directory, and this removes
	//   it. That is replace semantics rather than merge semantics, which is a
	//   defensible choice, but it is the caller's data and nothing in the API says
	//   so.
	if err := os.RemoveAll(localPath); err != nil {
		return fmt.Errorf("failed to clear destination directory %q: %w", localPath, err)
	}
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	progress := &s3Progress{reporter: core.ReporterFrom(ctx)}

	return forEachConcurrently(ctx, len(matched), func(ctx context.Context, n int) error {
		if skip[n] {
			return nil
		}
		i := matched[n]
		obj := objects[i]

		// A transfer of thousands of objects reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindObject, Name: obj.Key, Index: i + 1, Total: len(objects)}

		// A folder marker is the folder, not a file in it.
		if places[n].Folder {
			if err := os.MkdirAll(places[n].Local, 0755); err != nil {
				return item.Fail(ActionDownload, err)
			}
			progress.done(0, obj.Key)
			return nil
		}
		localFile := places[n].Local

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(localFile), 0755); err != nil {
			return item.Fail(ActionDownload, err)
		}

		if err := withRetry(ctx, func() error { return e.downloadFile(ctx, obj.Key, localFile) }); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return item.Fail(ActionDownload, err)
		}

		progress.done(obj.Size, obj.Key)
		return nil
	})
}

// uploadFile uploads a single file using presigned URL.
func (e *S3Executor) uploadFile(ctx context.Context, localPath, s3Key string) error {
	result, err := e.Provider.GeneratePresignedURL("upload", s3Key)
	if err != nil {
		return fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	file, err := os.Open(localPath)
	if err != nil {
		return permanent(fmt.Errorf("failed to open file: %w", err))
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return permanent(fmt.Errorf("failed to stat file: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, result.URL, file)
	if err != nil {
		return permanent(fmt.Errorf("failed to create request: %w", err))
	}
	req.ContentLength = stat.Size()

	// Apply CSP-specific headers provided by Tumblebug (e.g. x-ms-blob-type for Azure).
	for k, v := range result.RequiredHeaders {
		req.Header.Set(k, v)
	}

	resp, err := httpClientFor(e.Provider).Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("upload", resp)
	}

	return nil
}

// downloadFile downloads a single file using presigned URL.
func (e *S3Executor) downloadFile(ctx context.Context, s3Key, localPath string) error {
	result, err := e.Provider.GeneratePresignedURL("download", s3Key)
	if err != nil {
		return fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, result.URL, nil)
	if err != nil {
		return permanent(fmt.Errorf("failed to create request: %w", err))
	}

	resp, err := httpClientFor(e.Provider).Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("download", resp)
	}

	// Create truncates, so a retry after a broken body starts the file over.
	file, err := os.Create(localPath)
	if err != nil {
		return permanent(fmt.Errorf("failed to create file: %w", err))
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// S3StreamExecutor implements Executor for object storage → object storage
// transfers without staging: each object's download is the body of its upload.
//
// Against the relay through a local directory, the two legs overlap instead of
// following one another, and nothing the size of the bucket lands on local
// disk. The data still passes through this process, in memory, an object at a
// time per worker.
type S3StreamExecutor struct {
	Src S3Provider // where objects are read from
	Dst S3Provider // where they are written to
}

// NewS3StreamExecutor creates an S3StreamExecutor between two providers.
func NewS3StreamExecutor(src, dst S3Provider) *S3StreamExecutor {
	return &S3StreamExecutor{Src: src, Dst: dst}
}

// Execute copies every object under source's prefix that its filter keeps to
// the same relative key under destination's prefix — the keys the relay
// produces, so validation compares the two the same way.
func (e *S3StreamExecutor) Execute(ctx context.Context, source, destination DataLocation) error {
	if !source.IsObjectStorage() || !destination.IsObjectStorage() {
		return fmt.Errorf("stream transfer needs object storage on both sides")
	}

	_, srcPrefix := ParseBucketAndKey(source.Path)
	_, dstPrefix := ParseBucketAndKey(destination.Path)

	objects, err := e.Src.ListObjects(srcPrefix)
	if err != nil {
		return Obj{Kind: ObjectKindObject, Owner: srcPrefix}.Fail(ActionList, err)
	}

	var matched []int
	for i, obj := range objects {
		if keyUnderPrefix(srcPrefix, obj.Key) && source.Filter.MatchPath(filter.Rel(srcPrefix, obj.Key), obj.Size, false) {
			matched = append(matched, i)
		}
	}

	progress := &s3Progress{reporter: core.ReporterFrom(ctx)}

	return forEachConcurrently(ctx, len(matched), func(ctx context.Context, n int) error {
		i := matched[n]
		obj := objects[i]

		// A transfer of thousands of objects reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindObject, Name: obj.Key, Index: i + 1, Total: len(objects)}

		dstKey := streamDstKey(srcPrefix, dstPrefix, obj.Key)

		// Shared by every attempt at this object, so a retry counts only what
		// goes past the furthest an earlier attempt got.
		var credited int64
		copyOnce := func() error {
			return e.copyObject(ctx, obj, dstKey, func(pos int64) { progress.credit(&credited, pos, obj.Key) })
		}
		if err := withRetry(ctx, copyOnce); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return item.Fail(ActionUpload, err)
		}

		progress.finish(&credited, obj.Size, obj.Key)
		return nil
	})
}

// copyObject streams one object from Src to Dst, calling read with how far into
// the object the upload has read.
func (e *S3StreamExecutor) copyObject(ctx context.Context, obj ObjectInfo, dstKey string, read func(pos int64)) error {
	get, err := e.Src.GeneratePresignedURL("download", obj.Key)
	if err != nil {
		return fmt.Errorf("failed to generate presigned download URL: %w", err)
	}
	put, err := e.Dst.GeneratePresignedURL("upload", dstKey)
	if err != nil {
		return fmt.Errorf("failed to generate presigned upload URL: %w", err)
	}

	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, get.URL, nil)
	if err != nil {
		return permanent(fmt.Errorf("failed to create download request: %w", err))
	}
	resp, err := httpClientFor(e.Src).Do(getReq)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("download", resp)
	}

	// A presigned PUT needs its length up front. The response's own is the one
	// the body will match; the listing's stands in for a response that streams
	// without one.
	length := resp.ContentLength
	if length < 0 {
		length = obj.Size
	}

	var body io.Reader = &countingReader{r: resp.Body, read: read}
	if length == 0 {
		// A zero ContentLength with a body is sent chunked, which a presigned
		// PUT refuses. An empty object — a folder marker among them — is
		// sent with no body at all.
		body = http.NoBody
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, put.URL, body)
	if err != nil {
		return permanent(fmt.Errorf("failed to create upload request: %w", err))
	}
	putReq.ContentLength = length
	// Apply CSP-specific headers provided by Tumblebug (e.g. x-ms-blob-type for Azure).
	for k, v := range put.RequiredHeaders {
		putReq.Header.Set(k, v)
	}

	putResp, err := httpClientFor(e.Dst).Do(putReq)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return statusError("upload", putResp)
	}
	return nil
}

// streamDstKey maps a source key to its destination key: the part below the
// source prefix, put under the destination prefix. It is what the relay's
// download-then-upload produces, with one difference: a trailing slash — a
// folder marker's — is kept, where the relay's path cleaning dropped it.
func streamDstKey(srcPrefix, dstPrefix, key string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(key, srcPrefix), "/")
	if rel == "" {
		// The prefix named the object itself, not a folder above it.
		rel = path.Base(key)
	}
	dst := path.Join(dstPrefix, rel)
	if strings.HasSuffix(rel, "/") {
		dst += "/"
	}
	return dst
}

// countingReader passes reads through, telling read the running position.
type countingReader struct {
	r    io.Reader
	pos  int64
	read func(pos int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.pos += int64(n)
		c.read(c.pos)
	}
	return n, err
}

// forEachConcurrently calls fn for every index in [0, n), at most
// s3Concurrency at a time.
//
// The first failure cancels the ctx the others run under, and is the error
// returned: the ones it cut short fail with a cancellation that comes second.
// A cancellation from outside with nothing failing still returns the context
// error, since the indices never started were not transferred.
func forEachConcurrently(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s3Concurrency)

	for i := 0; i < n; i++ {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			// A slot can free up after the cancellation that should stop it.
			if err := gctx.Err(); err != nil {
				return err
			}
			return fn(gctx, i)
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

// s3Progress totals what concurrent transfers have finished and reports it.
//
// Under one lock, so the totals a reporter sees only ever grow — two workers
// finishing together cannot report their sums in the wrong order.
type s3Progress struct {
	mu       sync.Mutex
	reporter core.Reporter
	bytes    int64
	files    int64
	reported int64 // bytes as of the last report
}

// s3ReportEvery is how far the bytes of objects in flight move between reports.
// Every read would be a report per 32 KB, from every worker.
const s3ReportEvery = 1 << 20

// done records one finished object of size bytes and reports the new totals.
func (p *s3Progress) done(size int64, item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytes += size
	p.files++
	p.report(item)
}

// credit counts an object in flight up to pos, past what *credited — that
// object's own count, over all its attempts — already holds. A retry re-reads
// from the start, and counts nothing until it passes where the last one got.
func (p *s3Progress) credit(credited *int64, pos int64, item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pos <= *credited {
		return
	}
	p.bytes += pos - *credited
	*credited = pos
	if p.bytes-p.reported >= s3ReportEvery {
		p.report(item)
	}
}

// finish records an object credit has been counting as done: whatever of its
// size the reads did not reach is added, and the new totals are reported.
func (p *s3Progress) finish(credited *int64, size int64, item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if size > *credited {
		p.bytes += size - *credited
		*credited = size
	}
	p.files++
	p.report(item)
}

// report sends the totals. The caller holds p.mu.
func (p *s3Progress) report(item string) {
	p.reported = p.bytes
	p.reporter.Report(core.Update{Bytes: p.bytes, Files: p.files, Item: item})
}

// permanentError marks a failure that trying again cannot fix — a local file
// that will not open, a 403 — so withRetry gives up on it at once.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return &permanentError{err: err} }

// statusError reads a failed response into an error. A 5xx, 408 or 429 is the
// server's passing trouble and stays retryable; any other status is permanent.
func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	err := fmt.Errorf("%s failed with status %d: %s", op, resp.StatusCode, string(body))
	switch {
	case resp.StatusCode >= 500,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode == http.StatusTooManyRequests:
		return err
	default:
		return permanent(err)
	}
}

// withRetry runs fn up to s3Attempts times, waiting 1s and then 2s between
// tries. Each try runs fn whole, so a new presigned URL is fetched with it.
// It stops early on a permanent error or a cancelled ctx.
func withRetry(ctx context.Context, fn func() error) error {
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		var perm *permanentError
		if errors.As(err, &perm) || ctx.Err() != nil {
			return err
		}
		if attempt == s3Attempts {
			return fmt.Errorf("%w (after %d attempts)", err, attempt)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
		}
	}
}

// listLocalFiles returns a list of files matching the filter.
func (e *S3Executor) listLocalFiles(path string, flt *FilterOption) ([]string, error) {
	var files []string

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	err = filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Relative to the walk root, which on a relay is the staging directory
		// holding a copy of the source tree — so a rule reads the same here as
		// it did on the rsync leg that filled it.
		if !flt.MatchPath(filter.Rel(path, filePath), info.Size(), false) {
			return nil
		}
		files = append(files, filePath)
		return nil
	})

	return files, err
}

// isDirectory checks if the path is a directory.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
