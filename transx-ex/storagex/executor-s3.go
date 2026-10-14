package storagex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

	progress := &s3Progress{reporter: core.ReporterFrom(ctx)}

	return forEachConcurrently(ctx, len(matched), func(ctx context.Context, n int) error {
		i := matched[n]
		obj := objects[i]

		// A transfer of thousands of objects reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindObject, Name: obj.Key, Index: i + 1, Total: len(objects)}

		relPath := strings.TrimPrefix(obj.Key, keyPrefix)
		relPath = strings.TrimPrefix(relPath, "/")
		localFile := filepath.Join(localPath, relPath)

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
}

// done records one finished object of size bytes and reports the new totals.
func (p *s3Progress) done(size int64, item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytes += size
	p.files++
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
