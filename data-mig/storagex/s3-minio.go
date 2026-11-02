package storagex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioProvider implements Provider using minio-go SDK.
// Supports: AWS S3, MinIO, Ceph, DigitalOcean Spaces, and other S3-compatible services.
type MinioProvider struct {
	client   *minio.Client
	bucket   string
	endpoint string
	useSSL   bool

	// tunnel and httpClient are set only when the store is reached through an
	// SSH host: both the SDK and the presigned transfers dial through tunnel.
	tunnel     *core.SSHDialer
	httpClient *http.Client
}

// NewMinioProvider creates a new MinioProvider from MinioConfig.
//
// With config.SSHTunnel set the provider holds an SSH connection, opened on
// first use, and the caller must release it with Close (or CloseS3Provider).
func NewMinioProvider(config *MinioConfig, bucket string) (*MinioProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("minio config is required")
	}

	useSSL := config.UseSSL
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKeyId, config.SecretAccessKey, ""),
		Secure: useSSL,
		Region: config.Region,
	}
	switch strings.ToLower(config.BucketLookup) {
	case "dns":
		opts.BucketLookup = minio.BucketLookupDNS
	case "path":
		opts.BucketLookup = minio.BucketLookupPath
	default:
		opts.BucketLookup = minio.BucketLookupAuto
	}

	// Only the dial changes: TLS, SNI and the Host header still name the
	// endpoint itself, so a certificate issued for it verifies as usual.
	var tunnel *core.SSHDialer
	var httpClient *http.Client
	if config.SSHTunnel != nil {
		tunnel = core.NewSSHDialer(config.SSHTunnel)
		transport := newPresignedTransport(http.DefaultTransport.(*http.Transport))
		transport.Proxy = nil // the SSH host is the only hop
		transport.DialContext = tunnel.DialContext
		opts.Transport = transport
		httpClient = &http.Client{Transport: transport}
	} else if transport, err := minio.DefaultTransport(useSSL); err == nil {
		// The SDK's own default keeps 16 idle connections per host, fewer than
		// the parts a multipart upload can have in flight (s3MaxConnections).
		transport.MaxIdleConnsPerHost = s3MaxConnections
		opts.Transport = transport
	}

	client, err := minio.New(config.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create minio client: %w", err)
	}

	return &MinioProvider{
		client:     client,
		bucket:     bucket,
		endpoint:   config.Endpoint,
		useSSL:     useSSL,
		tunnel:     tunnel,
		httpClient: httpClient,
	}, nil
}

// HTTPClient returns the client presigned URLs must be fetched with, or nil
// when the default client reaches the endpoint.
func (p *MinioProvider) HTTPClient() *http.Client {
	return p.httpClient
}

// Close releases the SSH connection of a tunnelled provider. It is a no-op
// otherwise and safe to call more than once.
func (p *MinioProvider) Close() error {
	if p.tunnel == nil {
		return nil
	}
	if t, ok := p.httpClient.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return p.tunnel.Close()
}

// GeneratePresignedURL generates a presigned URL for S3 operations.
func (p *MinioProvider) GeneratePresignedURL(action, key string) (PresignedURLResult, error) {
	ctx := context.Background()
	expires := 1 * time.Hour // Default expiration

	switch action {
	case "upload":
		u, err := p.client.PresignedPutObject(ctx, p.bucket, key, expires)
		if err != nil {
			return PresignedURLResult{}, fmt.Errorf("failed to generate presigned PUT URL: %w", err)
		}
		return PresignedURLResult{URL: u.String()}, nil

	case "download":
		u, err := p.client.PresignedGetObject(ctx, p.bucket, key, expires, nil)
		if err != nil {
			return PresignedURLResult{}, fmt.Errorf("failed to generate presigned GET URL: %w", err)
		}
		return PresignedURLResult{URL: u.String()}, nil

	default:
		return PresignedURLResult{}, fmt.Errorf("unsupported action: %s (use 'upload' or 'download')", action)
	}
}

// ListObjects lists objects in the bucket with the given prefix.
func (p *MinioProvider) ListObjects(prefix string) ([]ObjectInfo, error) {
	ctx := context.Background()
	var objects []ObjectInfo

	objectCh := p.client.ListObjects(ctx, p.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	for obj := range objectCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("error listing objects: %w", obj.Err)
		}
		objects = append(objects, ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified.Format(time.RFC3339),
			ETag:         obj.ETag,
		})
	}

	return objects, nil
}

// GetBucket returns the bucket name.
func (p *MinioProvider) GetBucket() string {
	return p.bucket
}

// UploadFile uploads a local file to S3.
func (p *MinioProvider) UploadFile(localPath, key string) error {
	ctx := context.Background()
	_, err := p.client.FPutObject(ctx, p.bucket, key, localPath, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	return nil
}

// DownloadFile downloads a file from S3 to local path.
func (p *MinioProvider) DownloadFile(key, localPath string) error {
	ctx := context.Background()
	err := p.client.FGetObject(ctx, p.bucket, key, localPath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	return nil
}

// ============================================================================
// Multipart upload
// ============================================================================

// singlePutLimit is where the provider switches from one presigned PUT to
// parts. minio-go itself refuses a single PUT above 5 GiB.
func (p *MinioProvider) singlePutLimit() int64 {
	return multipartThreshold
}

// startMultipart opens an S3 multipart upload of size bytes to key, through the
// SDK client — and so through the SSH tunnel when there is one.
func (p *MinioProvider) startMultipart(ctx context.Context, key string, size int64) (multipartUpload, error) {
	// The SDK's own rule: the smallest multiple of 16 MiB that fits the object
	// in 10,000 parts. The size must be the real one; -1 fixes it at 528 MiB.
	_, partSize, _, err := minio.OptimalPartInfo(size, 0)
	if err != nil {
		return nil, permanent(fmt.Errorf("object of %d bytes cannot be uploaded in parts: %w", size, err))
	}
	core := minio.Core{Client: p.client}
	uploadID, err := core.NewMultipartUpload(ctx, p.bucket, key, minio.PutObjectOptions{})
	if err != nil {
		return nil, minioError(fmt.Errorf("initiate multipart upload: %w", err))
	}
	return &minioUpload{
		core:     core,
		bucket:   p.bucket,
		key:      key,
		size:     size,
		uploadID: uploadID,
		part:     partSize,
		etags:    make([]string, (size+partSize-1)/partSize),
	}, nil
}

// minioUpload is one S3 multipart upload in progress.
type minioUpload struct {
	core     minio.Core
	bucket   string
	key      string
	size     int64
	uploadID string
	part     int64

	mu        sync.Mutex
	etags     []string // by part number - 1
	completed bool     // a complete request has been sent
}

func (u *minioUpload) partSize() int64 { return u.part }

func (u *minioUpload) uploadPart(ctx context.Context, n int, size int64, r io.Reader) error {
	// Seen as a plain reader the SDK sends the part once and leaves retrying to
	// runMultipart, which reopens it; a seekable one it would also retry itself,
	// up to minio.MaxRetry times inside each of runMultipart's attempts.
	part, err := u.core.PutObjectPart(ctx, u.bucket, u.key, u.uploadID, n, struct{ io.Reader }{r}, size, minio.PutObjectPartOptions{})
	if err != nil {
		return minioError(err)
	}
	u.mu.Lock()
	u.etags[n-1] = part.ETag
	u.mu.Unlock()
	return nil
}

func (u *minioUpload) complete(ctx context.Context) error {
	parts := make([]minio.CompletePart, len(u.etags))
	for i, etag := range u.etags {
		parts[i] = minio.CompletePart{PartNumber: i + 1, ETag: etag}
	}
	retry := u.completed
	u.completed = true

	_, err := u.core.CompleteMultipartUpload(ctx, u.bucket, u.key, u.uploadID, parts, minio.PutObjectOptions{})
	if err == nil {
		return nil
	}
	// An earlier complete that succeeded but lost its response leaves nothing
	// to complete: the upload is gone and the object is there.
	if retry && minio.ToErrorResponse(err).Code == "NoSuchUpload" {
		if info, serr := u.core.StatObject(ctx, u.bucket, u.key, minio.StatObjectOptions{}); serr == nil && info.Size == u.size {
			return nil
		}
	}
	return minioError(err)
}

func (u *minioUpload) abort(ctx context.Context) error {
	return u.core.AbortMultipartUpload(ctx, u.bucket, u.key, u.uploadID)
}

// minioError classifies a minio-go failure for withRetry.
func minioError(err error) error {
	return sdkError(err, minio.ToErrorResponse(err).StatusCode)
}
