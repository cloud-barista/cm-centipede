package migration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
	"github.com/rs/zerolog/log"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
	"github.com/cloud-barista/cm-centipede/pkg/client/honeybee"
	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/cloud-barista/cm-centipede/pkg/connsec"
)

const defaultObjectConcurrency = 4

// ToMinioS3Config maps a honeybee ObjectStorage config to a transx S3MinioConfig.
//
// It goes through ResolveS3Endpoint rather than copying the connection's fields
// across, because a honeybee connection carries only what its caller typed.
// cm-honeybee works out the endpoint, the signing region, the TLS setting and
// the bucket addressing style on every inspect of its own, and returns none of
// them: os_endpoint is optional for every provider whose host it builds itself,
// there is no field for the bucket addressing style at all, and os_use_ssl is
// the caller's wish rather than what the provider imposed. Reading the fields
// alone therefore produced an empty endpoint for every provider that needs none
// typed in — and, for Tencent, an endpoint that resolved and a bucket that could
// not be addressed.
//
// The provider comes from the connection's source group; see
// honeybee.GetConnectionInfo. A minio group must name one, so the empty case is
// a group registered before that was required: there the caller's own endpoint
// is used as before.
//
// A connection with os_access_type = "ssh-tunnel" names an endpoint only its SSH
// host can reach, so the host is passed on as the config's SSHTunnel and every
// connection to the store is opened from there.
func ToMinioS3Config(cfg honeybee.HoneybeeConnConfig) (*transxex.S3MinioConfig, error) {
	var s3cfg *transxex.S3MinioConfig
	if strings.TrimSpace(cfg.ProviderName) == "" {
		endpoint := cfg.Endpoint
		useSSL := cfg.UseSSL
		if strings.HasPrefix(endpoint, "https://") {
			endpoint = strings.TrimPrefix(endpoint, "https://")
			useSSL = true
		} else {
			endpoint = strings.TrimPrefix(endpoint, "http://")
		}
		s3cfg = &transxex.S3MinioConfig{
			Endpoint:        endpoint,
			AccessKeyId:     cfg.AccessKey,
			SecretAccessKey: cfg.SecretKey,
			Region:          cfg.Region,
			UseSSL:          useSSL,
		}
	} else {
		var err error
		s3cfg, err = ToInlineMinioConfig(commonmodel.MinioConnConfig{
			ProviderName:    cfg.ProviderName,
			Endpoint:        cfg.Endpoint,
			AccessKeyId:     cfg.AccessKey,
			SecretAccessKey: cfg.SecretKey,
			Region:          cfg.Region,
			UseSSL:          cfg.UseSSL,
		})
		if err != nil {
			return nil, err
		}
	}

	if t := cfg.OSTunnel; t != nil {
		s3cfg.SSHTunnel = &transxex.SSHConfig{
			Host:       t.Host,
			Port:       t.Port,
			Username:   t.User,
			PrivateKey: normalizePEMKey(t.PrivateKey),
		}
	}
	return s3cfg, nil
}

// tumblebugPathPrefix is the group every cb-tumblebug namespace route hangs off
// ("/tumblebug/ns" in its server.go). It is spelled out here because transx-ex's
// Tumblebug provider builds its URLs from "/ns/..." onwards and prepends nothing.
const tumblebugPathPrefix = "/tumblebug"

// tumblebugAPIBase turns a configured cb-tumblebug endpoint into the base
// transx-ex expects: the one it can append "/ns/{nsId}/..." to.
//
// The prefix belongs here rather than in cm-centipede.yaml so that the two
// external endpoints follow one rule: the beetle client likewise appends
// "/beetle" to its configured endpoint, which makes host:port the value both
// settings take.
//
// An endpoint that already carries the prefix is left unchanged.
func tumblebugAPIBase(endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if base == "" || strings.HasSuffix(base, tumblebugPathPrefix) {
		return base
	}
	return base + tumblebugPathPrefix
}

// ToTumblebugOSConfig maps a BeetleOSRef to a transx TumblebugConfig.
//
// transx-ex reaches a managed bucket through cb-tumblebug's ObjectStorage API,
// so a beetleObjectStorage connection resolves to that access type at transfer
// time. cb-tumblebug is an internal detail here: it is not a selectable
// connection source, and the nsId/osId pair comes from the beetle ref.
//
// The endpoint and Basic Auth credentials are sourced from cm-centipede.yaml.
func ToTumblebugOSConfig(ref commonmodel.BeetleOSRef) *transxex.TumblebugConfig {
	return &transxex.TumblebugConfig{
		Endpoint: tumblebugAPIBase(config.Conf.Tumblebug.Endpoint),
		NsId:     ref.NsID,
		OsId:     ref.OsID,
		Auth: &transxex.AuthConfig{
			AuthType: transxex.AuthTypeBasic,
			Basic: &transxex.BasicAuthConfig{
				Username: config.Conf.Tumblebug.Username,
				Password: config.Conf.Tumblebug.Password,
			},
		},
	}
}

// MinioObjectStorageLoc builds a transxex.StorageLocation for a MinIO (honeybee) bucket path.
// bucketPath format: "bucket-name" or "bucket-name/prefix/".
// Pass nil filter for no filtering.
func MinioObjectStorageLoc(s3cfg *transxex.S3MinioConfig, bucketPath string, filter *transxex.PathFilterOption) transxex.StorageLocation {
	return transxex.StorageLocation{
		StorageType: transxex.StorageTypeObjectStorage,
		Path:        bucketPath,
		ObjectStorage: &transxex.ObjectStorageAccess{
			AccessType: transxex.AccessTypeMinio,
			Minio:      s3cfg,
		},
		Filter: filter,
	}
}

// TumblebugObjectStorageLoc builds a transxex.StorageLocation for a cb-tumblebug Object Storage bucket.
func TumblebugObjectStorageLoc(tbCfg *transxex.TumblebugConfig, bucketPath string, filter *transxex.PathFilterOption) transxex.StorageLocation {
	return transxex.StorageLocation{
		StorageType: transxex.StorageTypeObjectStorage,
		Path:        bucketPath,
		ObjectStorage: &transxex.ObjectStorageAccess{
			AccessType: transxex.AccessTypeTumblebug,
			Tumblebug:  tbCfg,
		},
		Filter: filter,
	}
}

// regionInHost reports whether the provider needs cfg.Region — either to build
// its endpoint or to sign its requests. It matches cm-honeybee's regionRequired,
// which is what decides that a minio source group must name a region_name.
func regionInHost(provider string) bool {
	switch provider {
	case commonmodel.ProviderAWS, commonmodel.ProviderAlibaba, commonmodel.ProviderTencent,
		commonmodel.ProviderNCP, commonmodel.ProviderNHN, commonmodel.ProviderIBM:
		return true // the endpoint host contains the region
	case commonmodel.ProviderGCP:
		return true // region-less host, but the SDK still signs with a region
	}
	return false
}

// ResolveS3Endpoint derives the endpoint, signing region, TLS setting and bucket
// addressing style from a connection's provider. It mirrors cm-honeybee's
// resolveS3Endpoint so that the two systems address the same bucket the same
// way; keep the two in sync.
//
// Defaults are HTTPS, no signing region, and automatic bucket lookup. Each
// branch below changes only what that provider does differently.
//
// The region in the HOSTNAME and the region a request is SIGNED with are the
// same string for most providers and are not for all of them: NCP serves
// "kr.object.ncloudstorage.com" and signs "kr-standard". Where they differ the
// signing region is returned EMPTY rather than guessed, which makes minio-go ask
// the bucket for its location and sign with the answer — the same way cb-spider
// reaches these providers. Returning "us-east-1" instead would suppress that
// lookup and sign NCP wrongly, and spelling cfg.Region into both would build
// "kr-standard.object.ncloudstorage.com", a host that does not exist.
func ResolveS3Endpoint(cfg commonmodel.MinioConnConfig) (endpoint, region string, useSSL bool, bucketLookup string, err error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.ProviderName))

	// A scheme typed into the endpoint is an explicit statement about TLS, so it
	// wins over both the provider's fixed value and cfg.UseSSL.
	given := strings.TrimSpace(cfg.Endpoint)
	schemeGiven, schemeSSL := false, false
	switch {
	case strings.HasPrefix(given, "https://"):
		given, schemeGiven, schemeSSL = strings.TrimPrefix(given, "https://"), true, true
	case strings.HasPrefix(given, "http://"):
		given, schemeGiven, schemeSSL = strings.TrimPrefix(given, "http://"), true, false
	}

	useSSL = true
	region = ""
	hostRegion := strings.TrimSpace(cfg.Region)
	if regionInHost(provider) && hostRegion == "" {
		return "", "", false, "", fmt.Errorf("region is required for provider %s", provider)
	}

	switch provider {
	case commonmodel.ProviderAWS:
		endpoint = "s3." + hostRegion + ".amazonaws.com"
		region = hostRegion
	case commonmodel.ProviderAlibaba:
		endpoint = "oss-" + hostRegion + ".aliyuncs.com"
	case commonmodel.ProviderTencent:
		// The bucket name must carry the AppId ("<name>-<AppId>"), so the bucket
		// is addressed DNS-style.
		endpoint = "cos." + hostRegion + ".myqcloud.com"
		region = hostRegion
		bucketLookup = "dns"
	case commonmodel.ProviderNCP:
		endpoint = hostRegion + ".object.ncloudstorage.com"
	case commonmodel.ProviderNHN:
		endpoint = hostRegion + "-api-object-storage.nhncloudservice.com"
		region = hostRegion
	case commonmodel.ProviderIBM:
		endpoint = "s3." + hostRegion + ".cloud-object-storage.appdomain.cloud"
	case commonmodel.ProviderGCP:
		// Needs HMAC interoperability keys on the connection, not a service
		// account key.
		endpoint = "storage.googleapis.com"
		region = hostRegion
	case commonmodel.ProviderKT:
		endpoint = "obj-e-1.ktcloud.com"
	case commonmodel.ProviderAzure:
		// Blob has no S3 API, but transx-ex recognises this host and switches to
		// its azblob provider, so no other branch is needed. accessKeyId is the
		// storage account name here, and the endpoint cannot be overridden.
		account := strings.TrimSpace(cfg.AccessKeyId)
		if account == "" {
			return "", "", false, "", fmt.Errorf("accessKeyId is required for provider %s", provider)
		}
		return account + ".blob.core.windows.net", region, true, "", nil
	case commonmodel.ProviderOpenStack, commonmodel.ProviderOnPrem:
		// The only providers where the user supplies both the host and whether to
		// use TLS.
		if given == "" {
			return "", "", false, "", fmt.Errorf("endpoint is required for provider %s", provider)
		}
		return given, region, cfg.UseSSL || (schemeGiven && schemeSSL), cfg.BucketLookup, nil
	default:
		return "", "", false, "", fmt.Errorf("unsupported provider: %s", provider)
	}

	// An endpoint on a provider-built host overrides it, which covers zones other
	// than a hard-coded one, private gateways and VPC endpoints.
	if given != "" {
		endpoint = given
		if schemeGiven {
			useSSL = schemeSSL
		}
	}
	if cfg.BucketLookup != "" {
		bucketLookup = cfg.BucketLookup
	}
	return endpoint, region, useSSL, bucketLookup, nil
}

// ToInlineMinioConfig builds a transxex.S3MinioConfig from an inline
// MinioConnConfig, resolving the provider into a concrete endpoint first —
// transxex.S3MinioConfig has no provider field and expects resolved values.
func ToInlineMinioConfig(cfg commonmodel.MinioConnConfig) (*transxex.S3MinioConfig, error) {
	endpoint, region, useSSL, bucketLookup, err := ResolveS3Endpoint(cfg)
	if err != nil {
		return nil, err
	}
	return &transxex.S3MinioConfig{
		Endpoint:        endpoint,
		AccessKeyId:     cfg.AccessKeyId,
		SecretAccessKey: cfg.SecretAccessKey,
		Region:          region,
		UseSSL:          useSSL,
		BucketLookup:    bucketLookup,
	}, nil
}

// ResolveObjectStorageLoc maps a ConnectionRef to a transx StorageLocation for
// object storage. Supports honeybee (MinIO direct), beetleObjectStorage (managed
// bucket, reached through cb-tumblebug's API inside transx-ex) and minio (inline
// S3 credentials). Inline credentials are decrypted first.
func ResolveObjectStorageLoc(ref commonmodel.ConnectionRef, bucketPath string, filter *transxex.PathFilterOption) (transxex.StorageLocation, error) {
	ref, err := connsec.DecryptRef(ref)
	if err != nil {
		return transxex.StorageLocation{}, fmt.Errorf("decrypt connection ref: %w", err)
	}

	switch ref.Source {
	case commonmodel.ConnectionSourceHoneybee:
		if ref.Honeybee == nil {
			return transxex.StorageLocation{}, fmt.Errorf("honeybee ref is nil")
		}
		cfg, err := honeybee.GetConnectionInfo(*ref.Honeybee)
		if err != nil {
			return transxex.StorageLocation{}, fmt.Errorf("get honeybee connection: %w", err)
		}
		s3cfg, err := ToMinioS3Config(*cfg)
		if err != nil {
			return transxex.StorageLocation{}, err
		}
		return MinioObjectStorageLoc(s3cfg, bucketPath, filter), nil

	case commonmodel.ConnectionSourceBeetleObjectStorage:
		if ref.BeetleOS == nil {
			return transxex.StorageLocation{}, fmt.Errorf("beetleObjectStorage ref is nil")
		}
		return TumblebugObjectStorageLoc(ToTumblebugOSConfig(*ref.BeetleOS), bucketPath, filter), nil

	case commonmodel.ConnectionSourceMinio:
		if ref.Minio == nil {
			return transxex.StorageLocation{}, fmt.Errorf("minio config is nil")
		}
		s3cfg, err := ToInlineMinioConfig(*ref.Minio)
		if err != nil {
			return transxex.StorageLocation{}, err
		}
		return MinioObjectStorageLoc(s3cfg, bucketPath, filter), nil

	default:
		return transxex.StorageLocation{}, fmt.Errorf("unsupported source type for object storage migration: %s", ref.Source)
	}
}

// MigrateObjectStorage transfers each ObjectMigrationInfo bucket from an object-
// storage source to the destination. The destination is another bucket (S3→S3) or,
// when m.DstType is filesystem, an SSH filesystem path (objectstorage→filesystem
// cross-storage; transx handles the S3→local→ssh relay). Processes buckets in
// ascending Order. Up to defaultObjectConcurrency buckets are transferred
// concurrently. Emits a ProgressEvent per bucket. Returns ctx.Err() on
// cancellation; per-bucket transfer errors are reported via ProgressEvent only.
func MigrateObjectStorage(ctx context.Context, m targetmodel.MigrationObjectStorageModel, progressCh chan<- ProgressEvent) error {
	dstType := m.DstType
	if dstType == "" {
		dstType = commonmodel.StorageTypeObjectStorage
	}

	buckets := make([]targetmodel.ObjectMigrationInfo, len(m.Buckets))
	copy(buckets, m.Buckets)
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Order < buckets[j].Order
	})

	type result struct {
		srcPath    string
		durationMs int64
		err        error
	}

	sem := make(chan struct{}, defaultObjectConcurrency)
	resultCh := make(chan result, len(buckets))
	var wg sync.WaitGroup

	for _, b := range buckets {
		select {
		case <-ctx.Done():
			wg.Wait()
			progressCh <- ProgressEvent{
				ItemPath: b.SrcPath,
				Status:   "cancelled",
				Err:      ctx.Err(),
			}
			return ctx.Err()
		default:
		}

		wg.Add(1)
		go func(bucket targetmodel.ObjectMigrationInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()

			filter, filterErr := ToTransxFilter(bucket.Rules)
			if filterErr != nil {
				resultCh <- result{srcPath: bucket.SrcPath, durationMs: time.Since(start).Milliseconds(), err: fmt.Errorf("build filter: %w", filterErr)}
				return
			}

			srcLoc, err := ResolveObjectStorageLoc(m.SrcConnection, bucket.SrcPath, filter)
			if err != nil {
				resultCh <- result{srcPath: bucket.SrcPath, durationMs: time.Since(start).Milliseconds(), err: fmt.Errorf("resolve src: %w", err)}
				return
			}

			dstLoc, err := resolveTransxLoc(m.DstConnection, dstType, bucket.DstPath, nil)
			if err != nil {
				resultCh <- result{srcPath: bucket.SrcPath, durationMs: time.Since(start).Milliseconds(), err: fmt.Errorf("resolve dst: %w", err)}
				return
			}

			transferErr := transxex.TransferStorage(transxex.StorageMigrationModel{
				Source:      srcLoc,
				Destination: dstLoc,
			})

			resultCh <- result{
				srcPath:    bucket.SrcPath,
				durationMs: time.Since(start).Milliseconds(),
				err:        transferErr,
			}
		}(b)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for r := range resultCh {
		status := "success"
		if r.err != nil {
			status = "failed"
			log.Error().
				Str("srcPath", r.srcPath).
				Err(r.err).
				Msg("object storage bucket transfer failed")
		}
		progressCh <- ProgressEvent{
			ItemPath:   r.srcPath,
			Status:     status,
			DurationMs: r.durationMs,
			Err:        r.err,
		}
	}

	return nil
}
