package storagex

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// =============================================================================
// Multipart upload — for an object larger than one presigned PUT should carry
// =============================================================================
//
// A presigned URL carries one PUT, and most backends cap a single PUT: 5 GiB on
// AWS S3 and through minio-go, 5000 MiB on Azure's Put Blob. A provider that
// holds its own SDK client can write larger objects in parts instead, and does
// so above multipartThreshold — well below either cap, so a large transfer that
// breaks resends one part rather than the whole object.
//
// Below the threshold the presigned PUT is kept. An object written in one part
// keeps an MD5 ETag, which validation compares; a multipart ETag is "<md5>-N"
// and says nothing about the content.

// multipartThreshold is the size above which a multipartUploader writes in
// parts. Both SDKs choose between the two the same way — minio-go at its part
// size, azblob at 256 MB — and 128 MiB sits between them. A variable so tests
// can lower it.
var multipartThreshold int64 = 128 << 20

// multipartUploader is implemented by a provider that can write an object
// larger than one PUT carries, in parts through its own SDK.
type multipartUploader interface {
	// singlePutLimit is the largest object sent as a single presigned PUT;
	// a larger object goes multipart.
	singlePutLimit() int64
	// startMultipart opens an upload of size bytes to key.
	startMultipart(ctx context.Context, key string, size int64) (multipartUpload, error)
}

// multipartUpload is one object's upload in progress.
type multipartUpload interface {
	// partSize is the length of every part but the last.
	partSize() int64
	// uploadPart writes part n, counting from 1, of size bytes read from r.
	// It may be called for several parts at once.
	uploadPart(ctx context.Context, n int, size int64, r io.Reader) error
	// complete joins the parts written into the object.
	complete(ctx context.Context) error
	// abort discards the parts written. A provider that keeps no state for
	// an unfinished upload does nothing.
	abort(ctx context.Context) error
}

// runMultipart writes size bytes to key in parts. open returns the n bytes of
// the source starting at off; it is called again on every attempt at a part,
// so a retry reads the part from its start. done, when non-nil, is called with
// how many bytes of the object have been written whenever a part lands.
//
// Each part is retried on its own, so withRetry does not also wrap the object:
// that would multiply the attempts at a part to s3Attempts squared. A failure
// or a cancelled ctx aborts the upload, under a context of its own, so a
// cancelled transfer still discards what it left behind.
func runMultipart(ctx context.Context, mp multipartUploader, key string, size int64,
	open func(off, n int64) (io.ReadCloser, error), done func(written int64)) (err error) {
	var up multipartUpload
	if err := withRetry(ctx, func() error {
		var serr error
		up, serr = mp.startMultipart(ctx, key, size)
		return serr
	}); err != nil {
		return fmt.Errorf("start multipart upload: %w", err)
	}
	defer func() {
		if err != nil {
			_ = up.abort(context.WithoutCancel(ctx))
		}
	}()

	partSize := up.partSize()
	if partSize <= 0 {
		return permanent(fmt.Errorf("multipart upload chose part size %d", partSize))
	}
	parts := int((size + partSize - 1) / partSize)

	var written atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s3Parts(ctx))
	for i := range parts {
		if gctx.Err() != nil {
			break // a part has failed; the ones not yet started never will be
		}
		n, off := i+1, int64(i)*partSize
		length := min(partSize, size-off)
		g.Go(func() error {
			err := withRetry(gctx, func() error {
				r, err := open(off, length)
				if err != nil {
					return err
				}
				defer r.Close()
				return up.uploadPart(gctx, n, length, r)
			})
			if err != nil {
				return fmt.Errorf("part %d of %d: %w", n, parts, err)
			}
			if done != nil {
				done(written.Add(length))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := withRetry(ctx, func() error { return up.complete(ctx) }); err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}

// sdkError classifies an SDK's failure for withRetry the way statusError does
// a presigned one: a 5xx, 408 or 429 — or no response at all, a dropped
// connection — stays retryable, and any other status is permanent.
func sdkError(err error, status int) error {
	if err == nil {
		return nil
	}
	switch {
	case status == 0, status >= 500, status == 408, status == 429:
		return err
	default:
		return permanent(err)
	}
}

// sectionOpener opens the n bytes at off of f for runMultipart. Every part
// reads f at its own offset, so concurrent parts share one open file.
func sectionOpener(f io.ReaderAt) func(off, n int64) (io.ReadCloser, error) {
	return func(off, n int64) (io.ReadCloser, error) {
		return sectionReadCloser{io.NewSectionReader(f, off, n)}, nil
	}
}

// sectionReadCloser is a file section that stays seekable, so a provider whose
// SDK takes an io.ReadSeeker reads it in place instead of buffering it.
type sectionReadCloser struct{ *io.SectionReader }

func (sectionReadCloser) Close() error { return nil }
