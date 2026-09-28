package storagex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/filter"
)

// S3Executor implements Executor for S3 object storage transfers.
// Uses presigned URLs for authentication-free upload/download.
type S3Executor struct {
	Provider S3Provider // S3 provider for generating presigned URLs
}

// NewS3Executor creates a new S3Executor with the given provider.
func NewS3Executor(provider S3Provider) *S3Executor {
	return &S3Executor{
		Provider: provider,
	}
}

// Execute performs S3 transfer from source to destination.
func (e *S3Executor) Execute(ctx context.Context, source, destination DataLocation) error {
	// Determine transfer direction based on StorageType
	srcIsS3 := source.IsObjectStorage()
	dstIsS3 := destination.IsObjectStorage()

	switch {
	case !srcIsS3 && dstIsS3:
		// Filesystem -> S3 (upload)
		return e.upload(ctx, source.Path, destination.Path, source.Filter)
	case srcIsS3 && !dstIsS3:
		// S3 -> Filesystem (download)
		return e.download(ctx, source.Path, destination.Path, source.Filter)
	case srcIsS3 && dstIsS3:
		// S3 -> S3 (not implemented yet)
		return fmt.Errorf("S3 to S3 transfer not implemented")
	default:
		return fmt.Errorf("invalid transfer: both source and destination are filesystem")
	}
}

// upload transfers local files to S3.
func (e *S3Executor) upload(ctx context.Context, localPath, s3Path string, flt *FilterOption) error {
	files, err := e.listLocalFiles(localPath, flt)
	if err != nil {
		return Obj{Kind: ObjectKindFile, Owner: localPath}.Fail(ActionList, err)
	}

	// Parse bucket and key from s3Path (e.g., "bucket-name/prefix/")
	_, keyPrefix := ParseBucketAndKey(s3Path)

	basePath := localPath
	if !isDirectory(localPath) {
		basePath = filepath.Dir(localPath)
	}

	for i, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// A transfer of thousands of files reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindFile, Name: file, Index: i + 1, Total: len(files)}

		relPath, err := filepath.Rel(basePath, file)
		if err != nil {
			return item.Fail(ActionUpload, err)
		}

		// Use only the key prefix, not the full path including bucket name
		s3Key := filepath.Join(keyPrefix, relPath)
		s3Key = strings.ReplaceAll(s3Key, "\\", "/") // Normalize to forward slashes

		if err := e.uploadFile(file, s3Key); err != nil {
			return item.Fail(ActionUpload, err)
		}
	}

	return nil
}

// download transfers S3 objects to local filesystem.
func (e *S3Executor) download(ctx context.Context, s3Path, localPath string, flt *FilterOption) error {
	// Empty the destination before downloading, so the download replaces what is
	// there rather than merging into it.
	//
	// On a relay this is belt and braces: newStagingLocation hands every transfer
	// a directory of its own, so there is nothing to clear. It used to be the
	// thing that kept one relay's leftovers out of the next one's upload, and the
	// comment here said so — that job now belongs to the staging directory being
	// unique.
	//
	// ⚠ localPath is NOT always staging. On a direct objectstorage → filesystem
	//   transfer it is the caller's own destination directory, and this removes
	//   it. That is replace semantics rather than merge semantics, which is a
	//   defensible choice, but it is the caller's data and nothing in the API says
	//   so.
	if err := os.RemoveAll(localPath); err != nil {
		return fmt.Errorf("failed to clear destination directory %q: %w", localPath, err)
	}
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	// Parse bucket and key from s3Path (e.g., "bucket-name/prefix/")
	_, keyPrefix := ParseBucketAndKey(s3Path)

	objects, err := e.Provider.ListObjects(keyPrefix)
	if err != nil {
		return Obj{Kind: ObjectKindObject, Owner: keyPrefix}.Fail(ActionList, err)
	}

	for i, obj := range objects {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !flt.Match(filter.Item{Path: obj.Key, Name: filepath.Base(obj.Key), Size: obj.Size}) {
			continue
		}

		// A transfer of thousands of objects reports which one it stopped on,
		// and how far in it got.
		item := Obj{Kind: ObjectKindObject, Name: obj.Key, Index: i + 1, Total: len(objects)}

		relPath := strings.TrimPrefix(obj.Key, keyPrefix)
		relPath = strings.TrimPrefix(relPath, "/")
		localFile := filepath.Join(localPath, relPath)

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(localFile), 0755); err != nil {
			return item.Fail(ActionDownload, err)
		}

		if err := e.downloadFile(obj.Key, localFile); err != nil {
			return item.Fail(ActionDownload, err)
		}
	}

	return nil
}

// uploadFile uploads a single file using presigned URL.
func (e *S3Executor) uploadFile(localPath, s3Key string) error {
	result, err := e.Provider.GeneratePresignedURL("upload", s3Key)
	if err != nil {
		return fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, result.URL, file)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.ContentLength = stat.Size()

	// Apply CSP-specific headers provided by Tumblebug (e.g. x-ms-blob-type for Azure).
	for k, v := range result.RequiredHeaders {
		req.Header.Set(k, v)
	}

	resp, err := httpClientFor(e.Provider).Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// downloadFile downloads a single file using presigned URL.
func (e *S3Executor) downloadFile(s3Key, localPath string) error {
	result, err := e.Provider.GeneratePresignedURL("download", s3Key)
	if err != nil {
		return fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	resp, err := httpClientFor(e.Provider).Get(result.URL)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// listLocalFiles returns a list of files matching the filter.
func (e *S3Executor) listLocalFiles(path string, flt *FilterOption) ([]string, error) {
	var files []string

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	err = filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		item := filter.Item{Path: filePath, Name: info.Name(), Size: info.Size(), ModTime: info.ModTime()}
		if !flt.Match(item) {
			return nil
		}
		files = append(files, filePath)
		return nil
	})

	return files, err
}

// isDirectory checks if the path is a directory.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
