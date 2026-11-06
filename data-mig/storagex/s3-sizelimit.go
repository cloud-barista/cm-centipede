package storagex

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/cloud-barista/cm-centipede/data-mig/storagex/filter"
)

// =============================================================================
// Single-PUT size limit — checked before a transfer, not discovered during it
// =============================================================================
//
// A provider that writes only through one presigned PUT — cb-tumblebug and
// cb-spider hand out no URL for a part — cannot write an object larger than the
// cloud behind it takes in one request. Left to the transfer, such an object
// fails only once its upload has started, and withRetry sends it again from the
// start s3Attempts times: 15 GiB sent for one 5 GiB file that was never going
// to land. The preflight here lists the source first and refuses the transfer
// with nothing sent.

// presignedPutLimit is the limit assumed for a cloud whose own is not known:
// AWS S3's, which most S3-compatible stores copy. A variable so tests can
// lower it.
var presignedPutLimit int64 = 5 << 30

// presignedPutLimits holds the clouds whose single-PUT limit differs from
// presignedPutLimit, keyed by provider name in lower case — cb-tumblebug's
// spelling, and cb-spider's once lowered. aws, alibaba, tencent, ibm and nhn
// document 5 GB; kt documents nothing, and openstack's is its operator's
// max_file_size, 5 GiB unless changed.
var presignedPutLimits = map[string]int64{
	"azure": 5000 << 20,         // Put Blob, service version 2019-12-12 and later
	"gcp":   5 << 40,            // a single PUT carries the largest object GCS stores
	"ncp":   10_000_000_000_000, // PutObject takes any object up to the 10 TB maximum
}

// putLimitFor returns the single-PUT limit of the named cloud.
func putLimitFor(csp string) int64 {
	if n, ok := presignedPutLimits[strings.ToLower(strings.TrimSpace(csp))]; ok {
		return n
	}
	return presignedPutLimit
}

// singlePutLimiter is implemented by a provider that can only write an object
// in one presigned PUT, so an object above the limit cannot be written at all.
type singlePutLimiter interface {
	// singlePutLimit is the largest object one presigned PUT carries.
	singlePutLimit() int64
	// limitSource names where the limit came from, for the error that cites
	// it: "azure behind tumblebug", or "tumblebug (cloud provider unknown)".
	limitSource() string
}

// limitSourceOf names the cloud behind a provider of the given kind, or says
// it is not known.
func limitSourceOf(kind, csp string) string {
	if csp == "" {
		return kind + " (cloud provider unknown)"
	}
	return strings.ToLower(csp) + " behind " + kind
}

// sizedItem is one file or object a transfer will write, as a preflight sees it.
type sizedItem struct {
	Name string
	Size int64
}

// singlePutPreflight returns a Preflight that refuses the transfer when any item
// list yields is larger than dst can take in one PUT, or nil when dst has no
// such limit — a pipeline with a nil Preflight runs exactly as before. noun
// names the items in the error: "file" or "object".
func singlePutPreflight(dst S3Provider, noun string, list func(ctx context.Context) ([]sizedItem, error)) func(context.Context) error {
	// A provider that writes a large object in parts has no limit to enforce.
	// Its singlePutLimit is where it switches to parts, not what it can take.
	if _, ok := dst.(multipartUploader); ok {
		return nil
	}
	lim, ok := dst.(singlePutLimiter)
	if !ok {
		return nil
	}
	return func(ctx context.Context) error {
		items, err := list(ctx)
		if err != nil {
			return fmt.Errorf("list source for size check: %w", err)
		}
		return checkSinglePutLimit(items, noun, lim)
	}
}

// checkSinglePutLimit fails, naming them, when any of items is larger than lim
// takes in one PUT. An item of exactly the limit fits.
func checkSinglePutLimit(items []sizedItem, noun string, lim singlePutLimiter) error {
	limit := lim.singlePutLimit()
	var over []string
	for _, it := range items {
		if it.Size > limit {
			over = append(over, fmt.Sprintf("%s (%s)", it.Name, formatSize(it.Size)))
		}
	}
	if len(over) == 0 {
		return nil
	}

	const shown = 10
	msg := strings.Join(over[:min(len(over), shown)], ", ")
	if len(over) > shown {
		msg += fmt.Sprintf(", and %d more", len(over)-shown)
	}
	// Permanent: the same listing fails the same way however often it is tried.
	return permanent(fmt.Errorf("%d %s(s) exceed the %s single-PUT limit of %s (multipart upload is not supported): %s",
		len(over), noun, formatSize(limit), lim.limitSource(), msg))
}

// formatSize writes n in the largest binary unit it is a whole number of —
// 5 GiB, 5000 MiB — or, failing that, the largest it reaches, to one decimal.
func formatSize(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	for i := len(units) - 1; i > 0; i-- {
		if d := int64(1) << (10 * i); n >= d && n%d == 0 {
			return fmt.Sprintf("%d %s", n/d, units[i])
		}
	}
	for i := len(units) - 1; i > 0; i-- {
		if d := int64(1) << (10 * i); n >= d {
			return fmt.Sprintf("%.1f %s", float64(n)/float64(d), units[i])
		}
	}
	return fmt.Sprintf("%d B", n)
}

// fsSizes lists the files a filesystem source sends, filtered as the transfer
// filters them: listLocalFiles on a direct upload, the rsync leg on a relay.
// A remote source is read by one find, before rsync has copied anything.
func fsSizes(src DataLocation) func(ctx context.Context) ([]sizedItem, error) {
	return func(ctx context.Context) ([]sizedItem, error) {
		files, err := ListFiles(ctx, src)
		if err != nil {
			return nil, err
		}
		var items []sizedItem
		for _, f := range files {
			// A source that is one file lists it as its own root — "." walked
			// here, "" from find's %P — and the transfer sends it unfiltered.
			if f.Path == "." || f.Path == "" {
				items = append(items, sizedItem{Name: path.Base(src.Path), Size: f.Size})
				continue
			}
			if src.Filter.MatchPath(f.Path, f.Size, false) {
				items = append(items, sizedItem{Name: f.Path, Size: f.Size})
			}
		}
		return items, nil
	}
}

// objectSizes lists the objects an object storage source sends, selected as
// S3StreamExecutor and S3Executor.download select them.
func objectSizes(src DataLocation, p S3Provider) func(ctx context.Context) ([]sizedItem, error) {
	return func(ctx context.Context) ([]sizedItem, error) {
		_, prefix := ParseBucketAndKey(src.Path)
		objects, err := p.ListObjects(prefix)
		if err != nil {
			return nil, err
		}
		var items []sizedItem
		for _, o := range objects {
			if keyUnderPrefix(prefix, o.Key) && src.Filter.MatchPath(filter.Rel(prefix, o.Key), o.Size, false) {
				items = append(items, sizedItem{Name: o.Key, Size: o.Size})
			}
		}
		return items, nil
	}
}
