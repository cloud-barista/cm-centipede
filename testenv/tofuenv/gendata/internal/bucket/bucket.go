// Package bucket uploads generated dummy files to object storage using a MinIO
// universal S3-compatible client (cb-spider S3Manager pattern).
package bucket

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/generate"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/layout"
)

// ErrDataExists indicates the bucket already has objects under the base prefix.
var ErrDataExists = errors.New("bucket already contains data under prefix")

// Params configures a bucket upload run.
type Params struct {
	Provider         string
	Region           string
	Bucket           string
	AccessKey        string
	SecretKey        string
	EndpointOverride string
	BucketLookup     string
	BasePrefix       string // e.g. "gendata/"
	Concurrency      int

	// OnFile, when set, is called with the size of each object once it is
	// uploaded, so the caller can report progress. Called from the workers.
	OnFile func(size int64)
}

func (p Params) client() (Client, error) {
	ci, err := ResolveConn(p.Provider, p.Region,
		Cred{AccessKey: p.AccessKey, SecretKey: p.SecretKey},
		p.EndpointOverride, p.BucketLookup)
	if err != nil {
		return nil, err
	}
	return NewClient(ci)
}

// workers is the parallelism for the per-object calls, at least 1.
func (p Params) workers() int {
	if p.Concurrency < 1 {
		return 1
	}
	return p.Concurrency
}

// CheckExisting returns ErrDataExists if any object exists under BasePrefix.
func CheckExisting(ctx context.Context, p Params) error {
	up, err := p.client()
	if err != nil {
		return err
	}
	exists, err := up.HasObjects(ctx, p.Bucket, p.BasePrefix)
	if err != nil {
		return fmt.Errorf("bucket existence check: %w", err)
	}
	if exists {
		return fmt.Errorf("%w: s3://%s/%s", ErrDataExists, p.Bucket, p.BasePrefix)
	}
	return nil
}

// Upload uploads files under BasePrefix + the layout plan. Returns the count uploaded.
func Upload(ctx context.Context, p Params, files []generate.File, plan layout.Plan) (int, error) {
	up, err := p.client()
	if err != nil {
		return 0, err
	}
	conc := p.workers()

	type job struct {
		idx  int
		file generate.File
	}
	jobs := make(chan job)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		count    int
	)
	worker := func() {
		defer wg.Done()
		for j := range jobs {
			key := p.BasePrefix + plan.Rel(j.idx, j.file.Rel)
			err := uploadOne(ctx, up, p.Bucket, key, j.file.Abs)
			if err == nil && p.OnFile != nil {
				p.OnFile(j.file.Size)
			}
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("upload %s: %w", key, err)
				}
			} else {
				count++
			}
			mu.Unlock()
		}
	}
	wg.Add(conc)
	for i := 0; i < conc; i++ {
		go worker()
	}
	for i, f := range files {
		jobs <- job{idx: i, file: f}
	}
	close(jobs)
	wg.Wait()
	return count, firstErr
}

func uploadOne(ctx context.Context, up Client, bucket, key, localAbs string) error {
	f, err := os.Open(localAbs)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return up.Upload(ctx, bucket, key, f, fi.Size())
}

// Keys lists the object keys under prefix; an empty prefix covers the whole
// bucket. Used to report what a cleanup would delete before it deletes it.
func Keys(ctx context.Context, p Params, prefix string) ([]string, error) {
	c, err := p.client()
	if err != nil {
		return nil, err
	}
	keys, err := c.ListKeys(ctx, p.Bucket, prefix)
	if err != nil {
		return nil, fmt.Errorf("list s3://%s/%s: %w", p.Bucket, prefix, err)
	}
	return keys, nil
}

// RemoveAll deletes every object under prefix and returns how many went. An empty
// prefix empties the whole bucket, which is what a destroy needs on NCP: unlike
// aws_s3_bucket, ncloud_objectstorage_bucket has no force_destroy, and the API
// answers DeleteBucket with 409 BucketNotEmpty while anything is left in it.
//
// Deletion continues past a failed key so that one object nobody may delete does
// not hide how much of the bucket did empty; the first error is returned once the
// rest is done.
func RemoveAll(ctx context.Context, p Params, prefix string) (int, error) {
	c, err := p.client()
	if err != nil {
		return 0, err
	}
	keys, err := c.ListKeys(ctx, p.Bucket, prefix)
	if err != nil {
		return 0, fmt.Errorf("list s3://%s/%s: %w", p.Bucket, prefix, err)
	}
	if len(keys) == 0 {
		return 0, nil
	}

	jobs := make(chan string)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		count    int
	)
	worker := func() {
		defer wg.Done()
		for key := range jobs {
			err := c.Remove(ctx, p.Bucket, key)
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete %s: %w", key, err)
				}
			} else {
				count++
			}
			mu.Unlock()
		}
	}
	conc := p.workers()
	wg.Add(conc)
	for i := 0; i < conc; i++ {
		go worker()
	}
	for _, k := range keys {
		jobs <- k
	}
	close(jobs)
	wg.Wait()
	return count, firstErr
}
