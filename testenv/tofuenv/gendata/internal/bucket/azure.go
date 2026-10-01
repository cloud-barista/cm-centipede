package bucket

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// Azure Blob Storage does not speak the S3 API, so the MinIO client cannot reach
// it: SigV4 signing and the S3 list calls have no counterpart there. It gets its
// own Client implementation on the azblob SDK, ported from
// transx-ex/storagex/s3-azure.go - only the parts gendata needs, which leaves out
// SAS pre-signing and downloads.
//
// The vocabulary maps as: bucket -> container, object key -> blob name.
//
// The credentials mean something else than everywhere else in this package:
// AccessKey is the storage account name and SecretKey its base64 account key.

// azureBlobHosts lists the Blob service host suffixes of each Azure cloud. An
// endpoint carrying one of these is served by azureClient instead of minioClient.
var azureBlobHosts = []string{
	"blob.core.windows.net",       // Azure Public
	"blob.core.chinacloudapi.cn",  // Azure China
	"blob.core.usgovcloudapi.net", // Azure US Government
}

// IsAzureBlobEndpoint reports whether endpoint addresses Azure Blob Storage,
// with or without a scheme.
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
// endpoint and lowercases what is left.
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

// azureAccountFromEndpoint returns the storage account carried by the first label
// of a Blob endpoint ("acct.blob.core.windows.net" -> "acct"), or "" when the
// endpoint has no account label.
func azureAccountFromEndpoint(endpoint string) string {
	host := azureEndpointHost(endpoint)
	for _, suffix := range azureBlobHosts {
		if strings.HasSuffix(host, "."+suffix) {
			return strings.TrimSuffix(host, "."+suffix)
		}
	}
	return ""
}

type azureClient struct {
	client *azblob.Client
}

// newAzureClient builds a Blob client from resolved connection info. The account
// is taken from the endpoint host rather than from AccessKey: the shared key
// credential signs for one account and the service rejects the signature unless
// the URL host names that same account, so deriving both from one string is what
// keeps them from disagreeing.
func newAzureClient(ci S3ConnInfo) (Client, error) {
	host := azureEndpointHost(ci.Endpoint)
	if host == "" {
		return nil, fmt.Errorf("azure: endpoint is required")
	}
	account := azureAccountFromEndpoint(ci.Endpoint)
	if account == "" {
		return nil, fmt.Errorf("azure: no storage account in endpoint %q "+
			"(expected <account>.blob.core.windows.net)", ci.Endpoint)
	}
	if ci.SecretKey == "" {
		return nil, fmt.Errorf("azure: account key is required")
	}

	cred, err := azblob.NewSharedKeyCredential(account, ci.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("azure shared key credential (account %q): %w", account, err)
	}
	// Shared key credentials are only accepted over TLS, so the scheme is fixed
	// here rather than read from ci.UseSSL.
	c, err := azblob.NewClientWithSharedKeyCredential("https://"+host+"/", cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure blob client (%s): %w", host, err)
	}
	return &azureClient{client: c}, nil
}

// Upload writes one blob. size is ignored: UploadStream buffers and chunks the
// reader itself, unlike PutObject which wants the length up front.
func (a *azureClient) Upload(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	_, err := a.client.UploadStream(ctx, bucket, key, r, nil)
	return err
}

func (a *azureClient) HasObjects(ctx context.Context, bucket, prefix string) (bool, error) {
	opts := &azblob.ListBlobsFlatOptions{MaxResults: to.Ptr(int32(1))}
	if prefix != "" {
		opts.Prefix = to.Ptr(prefix)
	}
	pager := a.client.NewListBlobsFlatPager(bucket, opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return false, err
		}
		if len(page.Segment.BlobItems) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ListKeys returns every blob name under prefix. The flat listing is already
// recursive, so it matches what the MinIO client reports with Recursive set.
func (a *azureClient) ListKeys(ctx context.Context, bucket, prefix string) ([]string, error) {
	opts := &azblob.ListBlobsFlatOptions{}
	if prefix != "" {
		opts.Prefix = to.Ptr(prefix)
	}
	var keys []string
	pager := a.client.NewListBlobsFlatPager(bucket, opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name != nil {
				keys = append(keys, *item.Name)
			}
		}
	}
	return keys, nil
}

func (a *azureClient) Remove(ctx context.Context, bucket, key string) error {
	_, err := a.client.DeleteBlob(ctx, bucket, key, nil)
	return err
}
