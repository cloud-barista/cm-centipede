package validation

import (
	"fmt"
	"strings"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
)

// report.go keeps one folder's or bucket's findings readable however large the
// dataset is.
//
// Every check here runs per item, so a wholesale failure — a transfer that never
// started, a destination that was never written — produces one finding per file
// or object. On a dataset of any size that buries the one thing worth reading:
// what kind of failure it was. So findings are budgeted.
//
// Two kinds are budgeted differently, and the dividing line is whether knowing
// the path changes what anyone does about it.
//
//	failed   The path is the finding. You go and look at that object. Kept per
//	         item, capped, with a line saying how many were not shown.
//	warning  A property of the comparison rather than of any one item — an ETag
//	         that cannot be compared, a file the filter dropped that is on the
//	         target anyway. The count is the information and the paths are not,
//	         so these collapse into ONE line carrying a few example paths.
type findings struct {
	root    string // the folder or bucket path every summary line is filed under
	items   []model.ValidationDetailItem
	failed  int
	notices map[string][]string // warning message -> the paths that produced it
	order   []string            // notice messages, in first-seen order
}

// maxItemDetails is how many per-item findings one folder or bucket contributes.
// Twenty fits on a screen, and twenty is enough to see the shape of a failure —
// whether everything is missing, or one prefix is.
const maxItemDetails = 20

// maxNoticeSamples is how many example paths a collapsed warning carries. Enough
// to recognise a handful of odd items among many ordinary ones, few enough that
// the line stays one line.
const maxNoticeSamples = 5

func newFindings(root string) *findings {
	return &findings{root: root, notices: map[string][]string{}}
}

// fail records one item-level failure. Past the budget it is counted and not
// listed; summarise() reports the difference.
func (f *findings) fail(itemPath, message string) {
	f.failed++
	if f.failed > maxItemDetails {
		return
	}
	f.items = append(f.items, model.ValidationDetailItem{
		ItemPath: itemPath,
		Status:   "failed",
		Message:  message,
	})
}

// notice records one item under a collapsed warning. message identifies the
// kind; the paths are kept only to sample them.
func (f *findings) notice(message, itemPath string) {
	if _, seen := f.notices[message]; !seen {
		f.order = append(f.order, message)
	}
	f.notices[message] = append(f.notices[message], itemPath)
}

// summarise returns the findings: the listed failures, then one line per warning
// kind, then the truncation notice if there was one.
func (f *findings) summarise(skipped int, skippedMessage string) []model.ValidationDetailItem {
	out := f.items
	if f.failed > maxItemDetails {
		out = append(out, model.ValidationDetailItem{
			ItemPath: f.root,
			Status:   "failed",
			Message: fmt.Sprintf("%d failures in total, %d shown",
				f.failed, maxItemDetails),
		})
	}
	for _, message := range f.order {
		paths := f.notices[message]
		out = append(out, model.ValidationDetailItem{
			ItemPath: f.root,
			Status:   "warning",
			Message:  fmt.Sprintf("%d %s: %s", len(paths), message, sampleOf(paths)),
		})
	}
	if skipped > 0 {
		out = append(out, model.ValidationDetailItem{
			ItemPath: f.root,
			Status:   "skipped",
			Message:  fmt.Sprintf("%d %s", skipped, skippedMessage),
		})
	}
	return out
}

// sampleOf renders up to maxNoticeSamples paths, marking that there are more.
func sampleOf(paths []string) string {
	if len(paths) <= maxNoticeSamples {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:maxNoticeSamples], ", ") + ", ..."
}

// comparableETag reports whether an ETag can stand in for the object's content.
//
// S3 builds a multipart ETag as the MD5 of the concatenated part MD5s plus
// "-<partCount>", so it is a function of how the upload was cut into parts and
// not of the bytes alone: two stores that chose different part sizes report
// different ETags for identical content. The "-N" suffix is the marker, and it
// reaches us intact — minio-go's trimEtag strips the quotes and nothing else.
//
// The converse does not hold, which is why this is a whitelist rather than a
// test for that suffix. An SSE-KMS or SSE-C object carries an ETag that is not
// an MD5 of the data at all, and S3 never promised one in the first place — both
// look like an ordinary digest. Anything that is not a bare 32-character hex
// string is treated as not comparable, which covers them without naming them.
func comparableETag(etag string) bool {
	if len(etag) != 32 {
		return false
	}
	for i := 0; i < len(etag); i++ {
		switch c := etag[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
