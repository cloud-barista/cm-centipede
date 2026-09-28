package storagex

import (
	"io"
	"net/http"
	"strings"
)

// PresignedURLResult holds a presigned URL and any additional headers required for the request.
type PresignedURLResult struct {
	URL             string
	RequiredHeaders map[string]string // CSP-specific headers (e.g. x-ms-blob-type for Azure)
}

// S3Provider defines the interface for S3-compatible object storage operations.
//
// Four providers implement it, selected by NewS3Provider from the location:
//   - MinioProvider: AWS S3, MinIO and S3-compatible storage, via the minio-go SDK
//   - AzureProvider: Azure Blob Storage, via the azblob SDK
//   - SpiderProvider: through the CB-Spider Object Storage API
//   - TumblebugProvider: through the CB-Tumblebug Object Storage API
type S3Provider interface {
	// GeneratePresignedURL generates a presigned URL for upload or download.
	// action: "upload" or "download"
	// key: object key (file path within bucket)
	GeneratePresignedURL(action, key string) (PresignedURLResult, error)

	// ListObjects lists objects with the given prefix.
	ListObjects(prefix string) ([]ObjectInfo, error)

	// GetBucket returns the bucket/container name for this provider.
	GetBucket() string
}

// ObjectInfo represents metadata about a storage object.
type ObjectInfo struct {
	Key          string // Object key (path)
	Size         int64  // Size in bytes
	LastModified string // Last modified timestamp
	ETag         string // Entity tag (hash)
}

// MinioConfig defines configuration for S3-compatible storage access using minio-go SDK.
// Supports: AWS S3, MinIO, Ceph, DigitalOcean Spaces, etc.
//
// MinioConfig is defined here as it's S3-specific.
// SpiderConfig and TumblebugConfig are defined in model.go as they're shared
// with the rest of the package and re-exported by transxex.
type MinioConfig struct {
	Endpoint        string `json:"endpoint" validate:"required"`
	AccessKeyId     string `json:"accessKeyId" validate:"required"`
	SecretAccessKey string `json:"secretAccessKey" validate:"required"`
	// Region is the signing region; empty means minio-go discovers it from the
	// bucket's location. See S3MinioConfig.Region in model.go.
	Region string `json:"region,omitempty"`
	UseSSL bool   `json:"useSSL,omitempty" default:"true"`
	// BucketLookup: "" (auto) | "dns" | "path"
	BucketLookup string `json:"bucketLookup,omitempty"`
	// SSHTunnel: the SSH host Endpoint is reached through. See
	// S3MinioConfig.SSHTunnel in model.go.
	SSHTunnel *SSHConfig `json:"sshTunnel,omitempty"`
}

// httpClientProvider is implemented by a provider whose presigned URLs cannot be
// fetched with the default HTTP client — a MinioProvider behind an SSH tunnel,
// whose URLs name an endpoint only the SSH host can reach.
type httpClientProvider interface {
	HTTPClient() *http.Client
}

// httpClientFor returns the client presigned URLs from p must be fetched with.
func httpClientFor(p S3Provider) *http.Client {
	if hp, ok := p.(httpClientProvider); ok {
		if c := hp.HTTPClient(); c != nil {
			return c
		}
	}
	return http.DefaultClient
}

// CloseS3Provider releases what p holds open — the SSH connection of a
// tunnelled MinioProvider. Providers that hold nothing are a no-op, so it is
// safe to call on any provider NewS3Provider returned.
func CloseS3Provider(p S3Provider) error {
	if c, ok := p.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// ParseBucketAndKey parses the path into bucket and key components.
// Path format: "bucket-name/path/to/object" or "bucket-name/"
func ParseBucketAndKey(path string) (bucket, key string) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) == 0 {
		return "", ""
	}

	bucket = parts[0]
	if len(parts) > 1 {
		key = parts[1]
	}

	return bucket, key
}
