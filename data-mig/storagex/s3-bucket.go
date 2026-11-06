package storagex

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

// =============================================================================
// Bucket existence — asked of the store before a transfer writes into it
// =============================================================================
//
// No provider creates a bucket: a write into one that is not there fails with
// NoSuchBucket only once the transfer has started. BucketExists lets a caller
// ask first and refuse the migration with nothing sent.

// ErrBucketCheckUnsupported is returned by BucketExists for a provider that has
// no way to ask: cb-spider and cb-tumblebug, whose buckets are created and
// owned by whoever made the object storage resource.
var ErrBucketCheckUnsupported = errors.New("bucket existence check is not supported for this provider")

// bucketChecker is implemented by a provider that can ask the store whether its
// bucket is there.
type bucketChecker interface {
	// bucketExists reports false with a nil error only when the store answered
	// that the bucket does not exist. Any other failure — a refused permission,
	// an unreachable endpoint — is an error, since it says nothing either way.
	bucketExists(ctx context.Context) (bool, error)
}

// BucketExists reports whether the bucket named by loc's path exists.
//
// A false result with a nil error means the store said so. A denied permission
// (AWS answers HeadBucket with 403 without s3:ListBucket) is returned as an
// error, not as false: credentials that may only write could still transfer.
// Providers that cannot ask return ErrBucketCheckUnsupported.
func BucketExists(loc DataLocation) (bool, error) {
	p, err := NewS3Provider(loc)
	if err != nil {
		return false, err
	}
	defer CloseS3Provider(p)

	c, ok := p.(bucketChecker)
	if !ok {
		return false, ErrBucketCheckUnsupported
	}
	return c.bucketExists(context.Background())
}

func (p *MinioProvider) bucketExists(ctx context.Context) (bool, error) {
	ok, err := p.client.BucketExists(ctx, p.bucket)
	if err != nil {
		return false, fmt.Errorf("check bucket %q: %w", p.bucket, err)
	}
	return ok, nil
}

func (p *AzureProvider) bucketExists(ctx context.Context) (bool, error) {
	_, err := p.client.ServiceClient().NewContainerClient(p.container).GetProperties(ctx, nil)
	if err == nil {
		return true, nil
	}
	if bloberror.HasCode(err, bloberror.ContainerNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("check container %q: %w", p.container, azureError(err))
}
