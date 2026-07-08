package filesapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DownloadAndSaveFile downloads one attachment and writes it under the session uploads directory.
func (c *Client) DownloadAndSaveFile(ctx context.Context, attachment File, basePath string, sessionID string) DownloadResult {
	fullPath, ok := BuildDownloadPath(basePath, sessionID, attachment.RelativePath)
	if !ok {
		return DownloadResult{
			FileID:  attachment.FileID,
			Success: false,
			Error:   fmt.Sprintf("invalid file path: %s", attachment.RelativePath),
		}
	}

	content, err := c.DownloadFile(ctx, attachment.FileID)
	if err != nil {
		return DownloadResult{FileID: attachment.FileID, Path: fullPath, Success: false, Error: err.Error()}
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return DownloadResult{FileID: attachment.FileID, Path: fullPath, Success: false, Error: err.Error()}
	}
	if err := os.WriteFile(fullPath, content, 0o600); err != nil {
		return DownloadResult{FileID: attachment.FileID, Path: fullPath, Success: false, Error: err.Error()}
	}

	return DownloadResult{FileID: attachment.FileID, Path: fullPath, Success: true, BytesWritten: int64(len(content))}
}

// DownloadSessionFiles downloads all attachments with bounded concurrency.
func (c *Client) DownloadSessionFiles(ctx context.Context, files []File, basePath string, sessionID string, concurrency int) []DownloadResult {
	return parallelWithLimit(ctx, files, concurrency, func(ctx context.Context, file File, _ int) DownloadResult {
		return c.DownloadAndSaveFile(ctx, file, basePath, sessionID)
	})
}

// UploadSessionFiles uploads local files with bounded concurrency.
func (c *Client) UploadSessionFiles(ctx context.Context, files []LocalFile, concurrency int) []UploadResult {
	return parallelWithLimit(ctx, files, concurrency, func(ctx context.Context, file LocalFile, _ int) UploadResult {
		return c.UploadFile(ctx, file.Path, file.RelativePath)
	})
}

func parallelWithLimit[T any, R any](ctx context.Context, items []T, concurrency int, fn func(context.Context, T, int) R) []R {
	results := make([]R, len(items))
	if len(items) == 0 {
		return results
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(items) {
		concurrency = len(items)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index] = fn(ctx, items[index], index)
			}
		}()
	}

	for index := range items {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}
