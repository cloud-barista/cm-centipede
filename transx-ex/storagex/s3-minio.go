package storagex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
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
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil // the SSH host is the only hop
		transport.DialContext = tunnel.DialContext
		opts.Transport = transport
		httpClient = &http.Client{Transport: transport}
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
