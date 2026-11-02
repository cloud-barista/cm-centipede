package storagex

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// ============================================================================
// Azure Blob Storage Detection
// ============================================================================

// azureBlobHosts lists the Blob service host suffixes of each Azure cloud.
// An objectStorage location whose minio endpoint carries one of these suffixes
// is served by AzureProvider instead of MinioProvider: Azure Blob Storage does
// not speak the S3 API, so SigV4 signing and the S3 list/presign calls of
// minio-go cannot reach it.
var azureBlobHosts = []string{
	"blob.core.windows.net",       // Azure Public
	"blob.core.chinacloudapi.cn",  // Azure China
	"blob.core.usgovcloudapi.net", // Azure US Government
}

// IsAzureBlobEndpoint reports whether endpoint addresses Azure Blob Storage.
// The endpoint may carry a scheme or not ("acct.blob.core.windows.net" and
// "https://acct.blob.core.windows.net" both match).
func IsAzureBlobEndpoint(endpoint string) bool {
	host := azureEndpointHost(endpoint)
	for _, suffix := range azureBlobHosts {
		if strings.HasSuffix(host, "."+suffix) || host == suffix {
			return true
		}
	}
	return false
}

// azureEndpointHost strips the scheme, any path and any trailing dot from
// endpoint and lowercases the remaining host.
func azureEndpointHost(endpoint string) string {
	host := strings.TrimSpace(endpoint)
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// azureAccountFromEndpoint returns the storage account name carried by the
// first label of an Azure Blob endpoint ("acct.blob.core.windows.net" → "acct").
// Returns "" when the endpoint is not in that form.
func azureAccountFromEndpoint(endpoint string) string {
	host := azureEndpointHost(endpoint)
	for _, suffix := range azureBlobHosts {
		if strings.HasSuffix(host, "."+suffix) {
			return strings.TrimSuffix(host, "."+suffix)
		}
	}
	return ""
}

// ============================================================================
// Azure Blob Storage Provider
// ============================================================================

// AzureConfig defines Azure Blob Storage access using a shared key credential.
//
// Callers do not build this directly: NewS3Provider derives it from the
// objectStorage minio config when the endpoint addresses Azure Blob Storage,
// mapping accessKeyId to AccountName and secretAccessKey to AccountKey. The
// field names differ from MinioConfig because the values mean something else —
// an Azure account name is not an S3 access key id.
type AzureConfig struct {
	// Endpoint is the Blob service host, with or without a scheme
	// (e.g. "myaccount.blob.core.windows.net").
	Endpoint string `json:"endpoint" validate:"required"`

	// AccountName is the storage account. When empty it is derived from the
	// first label of Endpoint.
	AccountName string `json:"accountName,omitempty"`

	// AccountKey is the storage account access key (base64).
	AccountKey string `json:"accountKey" validate:"required"`

	// Expires is the SAS URL lifetime in seconds. Defaults to 3600.
	Expires int `json:"expires,omitempty" default:"3600"`
}

// AzureProvider implements S3Provider using the Azure Blob Storage SDK.
// A bucket is an Azure container and an object key is a blob name.
type AzureProvider struct {
	client    *azblob.Client
	cred      *azblob.SharedKeyCredential
	container string
	account   string
	host      string // Blob service host, no scheme (e.g. "acct.blob.core.windows.net")
	expires   int
}

// NewAzureProvider creates a new AzureProvider from AzureConfig.
// container is the Azure container that plays the role of the S3 bucket.
func NewAzureProvider(config *AzureConfig, container string) (*AzureProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("azure config is required")
	}
	host := azureEndpointHost(config.Endpoint)
	if host == "" {
		return nil, fmt.Errorf("azure endpoint is required")
	}
	if strings.TrimSpace(config.AccountKey) == "" {
		return nil, fmt.Errorf("azure account key is required")
	}
	if strings.TrimSpace(container) == "" {
		return nil, fmt.Errorf("azure container name is required")
	}

	// The shared key credential signs for one account, and the service rejects
	// the signature unless that account also owns the URL host. Prefer the host
	// label so the two can never disagree.
	account := azureAccountFromEndpoint(config.Endpoint)
	if account == "" {
		account = strings.TrimSpace(config.AccountName)
	}
	if account == "" {
		return nil, fmt.Errorf("azure account name is required: not derivable from endpoint %q", config.Endpoint)
	}

	cred, err := azblob.NewSharedKeyCredential(account, config.AccountKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create azure shared key credential: %w", err)
	}

	serviceURL := "https://" + host + "/"
	client, err := azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create azure blob client: %w", err)
	}

	expires := config.Expires
	if expires <= 0 {
		expires = 3600 // Default 1 hour
	}

	return &AzureProvider{
		client:    client,
		cred:      cred,
		container: container,
		account:   account,
		host:      host,
		expires:   expires,
	}, nil
}

// GeneratePresignedURL generates a SAS URL for blob operations.
// The "upload" action also returns the x-ms-blob-type header that Put Blob
// requires; without it the service answers 400 regardless of the SAS.
func (p *AzureProvider) GeneratePresignedURL(action, key string) (PresignedURLResult, error) {
	var permissions sas.BlobPermissions
	var headers map[string]string

	switch action {
	case "upload":
		permissions = sas.BlobPermissions{Write: true, Create: true}
		headers = map[string]string{"x-ms-blob-type": "BlockBlob"}

	case "download":
		permissions = sas.BlobPermissions{Read: true}

	default:
		return PresignedURLResult{}, fmt.Errorf("unsupported action: %s (use 'upload' or 'download')", action)
	}

	now := time.Now().UTC()
	values := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     now.Add(-5 * time.Minute), // tolerate clock skew
		ExpiryTime:    now.Add(time.Duration(p.expires) * time.Second),
		ContainerName: p.container,
		BlobName:      key,
		Permissions:   to.Ptr(permissions).String(),
	}

	query, err := values.SignWithSharedKey(p.cred)
	if err != nil {
		return PresignedURLResult{}, fmt.Errorf("failed to generate SAS token: %w", err)
	}

	return PresignedURLResult{
		URL:             p.blobURL(key) + "?" + query.Encode(),
		RequiredHeaders: headers,
	}, nil
}

// ListObjects lists blobs in the container with the given prefix.
// The flat listing is recursive, matching the S3 provider's behaviour.
func (p *AzureProvider) ListObjects(prefix string) ([]ObjectInfo, error) {
	ctx := context.Background()
	var objects []ObjectInfo

	opts := &azblob.ListBlobsFlatOptions{}
	if prefix != "" {
		opts.Prefix = to.Ptr(prefix)
	}

	pager := p.client.NewListBlobsFlatPager(p.container, opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("error listing blobs in container %q: %w", p.container, err)
		}
		for _, item := range page.Segment.BlobItems {
			objects = append(objects, azureBlobToObjectInfo(item))
		}
	}

	return objects, nil
}

// GetBucket returns the container name.
func (p *AzureProvider) GetBucket() string {
	return p.container
}

// UploadFile uploads a local file to the container.
func (p *AzureProvider) UploadFile(localPath, key string) error {
	ctx := context.Background()
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	if _, err := p.client.UploadFile(ctx, p.container, key, f, nil); err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	return nil
}

// DownloadFile downloads a blob to a local path.
func (p *AzureProvider) DownloadFile(key, localPath string) error {
	ctx := context.Background()
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	if _, err := p.client.DownloadFile(ctx, p.container, key, f, nil); err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	return nil
}

// ============================================================================
// Multipart upload — staged blocks
// ============================================================================

// azureMinBlockSize is the smallest block a staged upload uses. Blocks are
// grown above it only as far as 50,000 of them need to hold the object. A
// variable so tests can lower it.
var azureMinBlockSize int64 = 64 << 20

// singlePutLimit is where the provider switches from one presigned Put Blob to
// staged blocks. Put Blob itself takes up to 5000 MiB.
func (p *AzureProvider) singlePutLimit() int64 {
	return multipartThreshold
}

// startMultipart opens a staged block upload of size bytes to key. Nothing is
// created on the service until the first block lands.
func (p *AzureProvider) startMultipart(_ context.Context, key string, size int64) (multipartUpload, error) {
	block := max(azureMinBlockSize, (size+blockblob.MaxBlocks-1)/blockblob.MaxBlocks)
	block = (block + 1<<20 - 1) &^ (1<<20 - 1) // whole MiB
	if block > blockblob.MaxStageBlockBytes {
		return nil, permanent(fmt.Errorf("object of %d bytes exceeds %d blocks of %d bytes", size, blockblob.MaxBlocks, int64(blockblob.MaxStageBlockBytes)))
	}
	blocks := int((size + block - 1) / block)
	return &azureUpload{
		blob:  p.client.ServiceClient().NewContainerClient(p.container).NewBlockBlobClient(key),
		block: block,
		ids:   make([]string, blocks),
	}, nil
}

// azureUpload is one staged block upload in progress.
type azureUpload struct {
	blob  *blockblob.Client
	block int64
	ids   []string // block IDs by part number - 1, all the same length
}

func (u *azureUpload) partSize() int64 { return u.block }

// azureBlockID names part n. Every ID of one blob must be the same length, so
// the number is zero-padded.
func azureBlockID(n int) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%08d", n))
}

func (u *azureUpload) uploadPart(ctx context.Context, n int, size int64, r io.Reader) error {
	// StageBlock needs to seek its body. A file section seeks in place; a
	// stream is held in memory, one block of it.
	var body io.ReadSeekCloser
	if rs, ok := r.(io.ReadSeeker); ok {
		body = streaming.NopCloser(rs)
	} else {
		buf, err := io.ReadAll(io.LimitReader(r, size))
		if err != nil {
			return fmt.Errorf("read block %d: %w", n, err)
		}
		if int64(len(buf)) != size {
			return fmt.Errorf("block %d: read %d of %d bytes", n, len(buf), size)
		}
		body = streaming.NopCloser(bytes.NewReader(buf))
	}

	id := azureBlockID(n)
	if _, err := u.blob.StageBlock(ctx, id, body, nil); err != nil {
		return azureError(fmt.Errorf("stage block %d: %w", n, err))
	}
	u.ids[n-1] = id
	return nil
}

// complete commits the blocks in part order. Committing the same list again is
// harmless, so a retry after a lost response needs no special case.
func (u *azureUpload) complete(ctx context.Context) error {
	if _, err := u.blob.CommitBlockList(ctx, u.ids, nil); err != nil {
		return azureError(fmt.Errorf("commit block list: %w", err))
	}
	return nil
}

// abort does nothing: the service discards uncommitted blocks after seven days,
// and offers no call to discard them sooner.
func (u *azureUpload) abort(context.Context) error { return nil }

// azureError classifies an azblob failure for withRetry.
func azureError(err error) error {
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		return sdkError(err, re.StatusCode)
	}
	return sdkError(err, 0)
}

// ============================================================================
// Helpers
// ============================================================================

// blobURL builds the blob URL, percent-encoding each path segment while keeping
// the separators. net/url.PathEscape must not be used here: it escapes "/" as
// %2F, which would flatten a nested blob name into a single segment.
func (p *AzureProvider) blobURL(key string) string {
	u := &url.URL{Path: "/" + p.container + "/" + strings.TrimPrefix(key, "/")}
	return "https://" + p.host + u.EscapedPath()
}

// azureBlobToObjectInfo converts one Azure blob listing item to an ObjectInfo.
func azureBlobToObjectInfo(item *container.BlobItem) ObjectInfo {
	info := ObjectInfo{}
	if item.Name != nil {
		info.Key = *item.Name
	}
	if item.Properties == nil {
		return info
	}
	if item.Properties.ContentLength != nil {
		info.Size = *item.Properties.ContentLength
	}
	if item.Properties.LastModified != nil {
		info.LastModified = item.Properties.LastModified.Format(time.RFC3339)
	}
	if item.Properties.ETag != nil {
		// Azure quotes ETags; strip them so both providers report the same shape.
		info.ETag = strings.Trim(string(*item.Properties.ETag), `"`)
	}
	return info
}
