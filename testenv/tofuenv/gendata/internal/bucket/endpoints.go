package bucket

import (
	"fmt"
	"strings"
)

// Cred carries object-storage access credentials.
type Cred struct {
	AccessKey string
	SecretKey string
}

// S3ConnInfo is the resolved connection info for a MinIO S3-compatible client.
type S3ConnInfo struct {
	Provider       string
	Endpoint       string
	Region         string
	AccessKey      string
	SecretKey      string
	UseSSL         bool
	RegionRequired bool
	BucketLookup   string // "auto" | "dns" | "path"
}

// ResolveConn maps provider + region + credentials to S3 connection info,
// mirroring cb-spider api-runtime/common-runtime/S3Manager.go GetS3ConnectionInfo.
// Adding a CSP = adding a case with its S3-compatible endpoint. endpointOverride,
// when set, wins (custom / S3-compatible endpoints such as a local MinIO).
func ResolveConn(provider, region string, cred Cred, endpointOverride, bucketLookup string) (S3ConnInfo, error) {
	p := strings.ToUpper(strings.TrimSpace(provider))
	ci := S3ConnInfo{
		Provider:     p,
		Region:       region,
		AccessKey:    cred.AccessKey,
		SecretKey:    cred.SecretKey,
		UseSSL:       true,
		BucketLookup: bucketLookup,
	}
	switch p {
	case "AWS":
		ci.Endpoint = fmt.Sprintf("s3.%s.amazonaws.com", region)
		ci.RegionRequired = true
	case "GCP":
		ci.Endpoint = "storage.googleapis.com"
		ci.RegionRequired = true
	case "ALIBABA":
		ci.Endpoint = fmt.Sprintf("oss-%s.aliyuncs.com", region)
	case "TENCENT":
		ci.Endpoint = fmt.Sprintf("cos.%s.myqcloud.com", region)
		ci.RegionRequired = true
	case "NCP":
		// NCP hostnames use the lowercase region code (KR -> kr) while the S3
		// signature is computed against "<code>-standard" (kr-standard), which
		// differs from the ncloud provider region code.
		code := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(region)), "-standard")
		if code == "" {
			code = "kr"
		}
		ci.Endpoint = fmt.Sprintf("%s.object.ncloudstorage.com", code)
		ci.Region = code + "-standard"
		ci.RegionRequired = true
	case "NHN":
		ci.Endpoint = fmt.Sprintf("%s-api-object-storage.nhncloudservice.com", region)
		ci.RegionRequired = true
	case "IBM":
		ci.Endpoint = fmt.Sprintf("s3.%s.cloud-object-storage.appdomain.cloud", region)
	case "KT":
		ci.Endpoint = "obj-e-1.ktcloud.com"
	case "AZURE":
		// Blob storage has no S3 API; azure.go serves this endpoint with the
		// azblob SDK instead, and NewClient tells the two apart by the host.
		//
		// The credentials mean something else here: AccessKey is the storage
		// account name and SecretKey its base64 account key. Region and
		// BucketLookup have no counterpart and stay unset.
		account := strings.TrimSpace(ci.AccessKey)
		if account == "" {
			return S3ConnInfo{}, fmt.Errorf(
				"provider %q needs the storage account name as the access key", provider)
		}
		ci.Endpoint = account + ".blob.core.windows.net"
	default:
		return S3ConnInfo{}, fmt.Errorf("provider %q is not supported for object storage yet", provider)
	}

	// AZURE is excluded: its host is derived from the storage account, and a host
	// naming a different account could not be signed for anyway - a shared key
	// signature is only valid for its own account. Clouds other than the public
	// one are therefore out of reach here.
	if endpointOverride != "" && p != "AZURE" {
		if strings.HasPrefix(endpointOverride, "http://") {
			ci.UseSSL = false
		}
		ci.Endpoint = strings.TrimPrefix(strings.TrimPrefix(endpointOverride, "https://"), "http://")
		ci.Endpoint = strings.TrimSuffix(ci.Endpoint, "/")
	}
	if ci.AccessKey == "" || ci.SecretKey == "" {
		return S3ConnInfo{}, fmt.Errorf("missing credentials for provider %q", provider)
	}
	return ci, nil
}
