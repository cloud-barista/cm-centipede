package storagex

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// testAccountKey is a syntactically valid (base64) dummy storage account key.
// NewSharedKeyCredential only decodes it; no request is ever sent.
var testAccountKey = base64.StdEncoding.EncodeToString([]byte("cm-centipede-transx-test-account-key"))

func TestIsAzureBlobEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		want     bool
	}{
		{"acct.blob.core.windows.net", true},
		{"https://acct.blob.core.windows.net", true},
		{"https://acct.blob.core.windows.net/", true},
		{"http://ACCT.Blob.Core.Windows.Net/container", true},
		{"acct.blob.core.chinacloudapi.cn", true},
		{"acct.blob.core.usgovcloudapi.net", true},
		{"blob.core.windows.net", true}, // service host without an account label
		{"play.min.io", false},
		{"s3.amazonaws.com", false},
		{"localhost:9000", false},
		{"acct.file.core.windows.net", false}, // Files, not Blob
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.endpoint, func(t *testing.T) {
			if got := IsAzureBlobEndpoint(c.endpoint); got != c.want {
				t.Errorf("IsAzureBlobEndpoint(%q) = %v, want %v", c.endpoint, got, c.want)
			}
		})
	}
}

func TestAzureAccountFromEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{"acct.blob.core.windows.net", "acct"},
		{"https://acct.blob.core.windows.net/", "acct"},
		{"https://ACCT.blob.core.windows.net", "acct"},
		{"acct.blob.core.chinacloudapi.cn", "acct"},
		{"blob.core.windows.net", ""}, // no account label
		{"play.min.io", ""},
	}
	for _, c := range cases {
		t.Run(c.endpoint, func(t *testing.T) {
			if got := azureAccountFromEndpoint(c.endpoint); got != c.want {
				t.Errorf("azureAccountFromEndpoint(%q) = %q, want %q", c.endpoint, got, c.want)
			}
		})
	}
}

func TestNewAzureProviderValidation(t *testing.T) {
	cases := []struct {
		name      string
		config    *AzureConfig
		container string
		wantErr   string
	}{
		{"nil config", nil, "c", "azure config is required"},
		{"empty endpoint", &AzureConfig{AccountKey: testAccountKey}, "c", "azure endpoint is required"},
		{"empty key", &AzureConfig{Endpoint: "acct.blob.core.windows.net"}, "c", "azure account key is required"},
		{
			"empty container",
			&AzureConfig{Endpoint: "acct.blob.core.windows.net", AccountKey: testAccountKey},
			"",
			"azure container name is required",
		},
		{
			"account not derivable",
			&AzureConfig{Endpoint: "blob.core.windows.net", AccountKey: testAccountKey},
			"c",
			"azure account name is required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewAzureProvider(c.config, c.container)
			if err == nil {
				t.Fatalf("NewAzureProvider() = nil error, want %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("NewAzureProvider() error = %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

// TestNewAzureProviderAccountPrecedence checks that the endpoint host label wins
// over the configured account name: the shared key credential signs for one
// account and the service rejects the signature unless the URL host matches.
func TestNewAzureProviderAccountPrecedence(t *testing.T) {
	p, err := NewAzureProvider(&AzureConfig{
		Endpoint:    "https://fromhost.blob.core.windows.net/",
		AccountName: "fromconfig",
		AccountKey:  testAccountKey,
	}, "media")
	if err != nil {
		t.Fatalf("NewAzureProvider() error = %v", err)
	}
	if p.account != "fromhost" {
		t.Errorf("account = %q, want %q", p.account, "fromhost")
	}
	if p.host != "fromhost.blob.core.windows.net" {
		t.Errorf("host = %q, want %q", p.host, "fromhost.blob.core.windows.net")
	}
	if got := p.GetBucket(); got != "media" {
		t.Errorf("GetBucket() = %q, want %q", got, "media")
	}
	if p.expires != 3600 {
		t.Errorf("expires = %d, want 3600 by default", p.expires)
	}
}

// TestAzureBlobURL fixes the escaping rule: separators inside a nested blob name
// stay separators. url.PathEscape would turn them into %2F and flatten the name.
func TestAzureBlobURL(t *testing.T) {
	p := newTestAzureProvider(t, "media")

	cases := []struct {
		key  string
		want string
	}{
		{"a.txt", "https://acct.blob.core.windows.net/media/a.txt"},
		{"sample/logs/a.txt", "https://acct.blob.core.windows.net/media/sample/logs/a.txt"},
		{"/leading.txt", "https://acct.blob.core.windows.net/media/leading.txt"},
		{"with space.txt", "https://acct.blob.core.windows.net/media/with%20space.txt"},
		{"dir/한글.txt", "https://acct.blob.core.windows.net/media/dir/%ED%95%9C%EA%B8%80.txt"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := p.blobURL(c.key); got != c.want {
				t.Errorf("blobURL(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

func TestAzureGeneratePresignedURL(t *testing.T) {
	p := newTestAzureProvider(t, "media")

	t.Run("upload", func(t *testing.T) {
		res, err := p.GeneratePresignedURL("upload", "sample/logs/a.txt")
		if err != nil {
			t.Fatalf("GeneratePresignedURL() error = %v", err)
		}
		u := parseSAS(t, res.URL)
		if u.Path != "/media/sample/logs/a.txt" {
			t.Errorf("path = %q, want %q", u.Path, "/media/sample/logs/a.txt")
		}
		// Put Blob rejects the request without this header, SAS or not.
		if got := res.RequiredHeaders["x-ms-blob-type"]; got != "BlockBlob" {
			t.Errorf("x-ms-blob-type = %q, want %q", got, "BlockBlob")
		}
		if sp := u.Query().Get("sp"); !strings.Contains(sp, "w") || !strings.Contains(sp, "c") {
			t.Errorf("sp = %q, want it to grant write and create", sp)
		}
	})

	t.Run("download", func(t *testing.T) {
		res, err := p.GeneratePresignedURL("download", "a.txt")
		if err != nil {
			t.Fatalf("GeneratePresignedURL() error = %v", err)
		}
		u := parseSAS(t, res.URL)
		if u.Path != "/media/a.txt" {
			t.Errorf("path = %q, want %q", u.Path, "/media/a.txt")
		}
		if len(res.RequiredHeaders) != 0 {
			t.Errorf("RequiredHeaders = %v, want none for download", res.RequiredHeaders)
		}
		if sp := u.Query().Get("sp"); sp != "r" {
			t.Errorf("sp = %q, want %q", sp, "r")
		}
	})

	t.Run("unsupported action", func(t *testing.T) {
		if _, err := p.GeneratePresignedURL("delete", "a.txt"); err == nil {
			t.Error("GeneratePresignedURL(\"delete\") = nil error, want an error")
		}
	})
}

// TestNewS3ProviderRoutesAzure checks that an objectStorage location typed
// "minio" resolves to the Azure provider when its endpoint says so.
func TestNewS3ProviderRoutesAzure(t *testing.T) {
	loc := func(endpoint string) DataLocation {
		return DataLocation{
			StorageType: StorageTypeObjectStorage,
			Path:        "media/logs/",
			ObjectStorage: &ObjectStorageAccess{
				AccessType: AccessTypeMinio,
				Minio: &S3MinioConfig{
					Endpoint:        endpoint,
					AccessKeyId:     "acct",
					SecretAccessKey: testAccountKey,
					UseSSL:          true,
				},
			},
		}
	}

	azure, err := NewS3Provider(loc("acct.blob.core.windows.net"))
	if err != nil {
		t.Fatalf("NewS3Provider(azure) error = %v", err)
	}
	if _, ok := azure.(*AzureProvider); !ok {
		t.Errorf("NewS3Provider(azure) = %T, want *AzureProvider", azure)
	}
	if got := azure.GetBucket(); got != "media" {
		t.Errorf("GetBucket() = %q, want %q", got, "media")
	}

	s3, err := NewS3Provider(loc("play.min.io"))
	if err != nil {
		t.Fatalf("NewS3Provider(s3) error = %v", err)
	}
	if _, ok := s3.(*MinioProvider); !ok {
		t.Errorf("NewS3Provider(s3) = %T, want *MinioProvider", s3)
	}
}

func newTestAzureProvider(t *testing.T, container string) *AzureProvider {
	t.Helper()
	p, err := NewAzureProvider(&AzureConfig{
		Endpoint:   "acct.blob.core.windows.net",
		AccountKey: testAccountKey,
	}, container)
	if err != nil {
		t.Fatalf("NewAzureProvider() error = %v", err)
	}
	return p
}

func parseSAS(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	if u.Scheme != "https" || u.Host != "acct.blob.core.windows.net" {
		t.Errorf("scheme://host = %s://%s, want https://acct.blob.core.windows.net", u.Scheme, u.Host)
	}
	if u.Query().Get("sig") == "" {
		t.Error("SAS URL has no sig parameter")
	}
	if u.Query().Get("se") == "" {
		t.Error("SAS URL has no se (expiry) parameter")
	}
	return u
}
