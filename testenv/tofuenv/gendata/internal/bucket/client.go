package bucket

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client is the object-storage surface gendata needs: the upload itself, plus
// the listing and deletion behind the pre-flight check and the cleanup path.
type Client interface {
	Upload(ctx context.Context, bucket, key string, r io.Reader, size int64) error
	// HasObjects reports whether at least one object exists under prefix.
	HasObjects(ctx context.Context, bucket, prefix string) (bool, error)
	// ListKeys returns every object key under prefix; an empty prefix lists the
	// whole bucket.
	ListKeys(ctx context.Context, bucket, prefix string) ([]string, error)
	// Remove deletes a single object.
	Remove(ctx context.Context, bucket, key string) error
}

type minioClient struct {
	client *minio.Client
}

func lookupType(s string) minio.BucketLookupType {
	switch s {
	case "dns":
		return minio.BucketLookupDNS
	case "path":
		return minio.BucketLookupPath
	default:
		return minio.BucketLookupAuto
	}
}

// NewClient builds the client for the resolved connection info: MinIO for the S3
// endpoints, azblob for Azure Blob Storage. The endpoint is what tells them apart,
// as in transx-ex/storagex/planner.go - Blob answers on the same connection info
// but does not speak S3, so minio-go cannot reach it at all.
func NewClient(ci S3ConnInfo) (Client, error) {
	if IsAzureBlobEndpoint(ci.Endpoint) {
		return newAzureClient(ci)
	}
	opts := &minio.Options{
		Creds:        credentials.NewStaticV4(ci.AccessKey, ci.SecretKey, ""),
		Secure:       ci.UseSSL,
		BucketLookup: lookupType(ci.BucketLookup),
	}
	if ci.RegionRequired && ci.Region != "" {
		opts.Region = ci.Region
	}
	c, err := minio.New(ci.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("minio client (%s): %w", ci.Endpoint, err)
	}
	return &minioClient{client: c}, nil
}

func (u *minioClient) Upload(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	_, err := u.client.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

func (u *minioClient) HasObjects(ctx context.Context, bucket, prefix string) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for obj := range u.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, MaxKeys: 1}) {
		if obj.Err != nil {
			return false, obj.Err
		}
		return true, nil
	}
	return false, nil
}

// ListKeys walks the bucket recursively, so what comes back are object keys and
// never the synthetic directory entries a non-recursive listing reports.
func (u *minioClient) ListKeys(ctx context.Context, bucket, prefix string) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var keys []string
	for obj := range u.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// Remove deletes one object per call on purpose. minio-go's RemoveObjects would
// batch the keys into Multi-Object Delete requests, which NCP Object Storage does
// not list among the S3 operations it supports.
func (u *minioClient) Remove(ctx context.Context, bucket, key string) error {
	return u.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}
