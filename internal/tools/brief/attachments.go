package brief

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Attachment is locally resolved metadata retained for host and UI consumers.
type Attachment struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsImage  bool   `json:"isImage"`
	FileUUID string `json:"file_uuid,omitempty"`
}

// Uploader optionally makes an attachment available to a remote host.
type Uploader interface {
	Upload(context.Context, string, int64) (string, error)
}

// UploaderFunc adapts a function to Uploader.
type UploaderFunc func(context.Context, string, int64) (string, error)

// Upload calls f with the resolved attachment path and byte size.
func (f UploaderFunc) Upload(ctx context.Context, path string, size int64) (string, error) {
	return f(ctx, path, size)
}

type attachmentConfig struct {
	getwd       func() (string, error)
	userHomeDir func() (string, error)
	stat        func(string) (fs.FileInfo, error)
	uploader    Uploader
}

func resolveAttachments(ctx context.Context, rawPaths []string, config attachmentConfig) ([]Attachment, error) {
	cwd, err := config.getwd()
	if err != nil {
		return nil, fmt.Errorf("get current working directory: %w", err)
	}
	cwd = filepath.Clean(cwd)

	attachments := make([]Attachment, 0, len(rawPaths))
	for _, rawPath := range rawPaths {
		fullPath, err := expandPath(rawPath, cwd, config.userHomeDir)
		if err != nil {
			return nil, err
		}
		info, err := config.stat(fullPath)
		if err != nil {
			return nil, attachmentStatError(rawPath, cwd, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Attachment \"%s\" is not a regular file.", rawPath)
		}
		attachments = append(attachments, Attachment{
			Path:    fullPath,
			Size:    info.Size(),
			IsImage: isImagePath(fullPath),
		})
	}

	uploadAttachments(ctx, attachments, config.uploader)
	return attachments, nil
}

func expandPath(rawPath, cwd string, userHomeDir func() (string, error)) (string, error) {
	if strings.ContainsRune(rawPath, '\x00') || strings.ContainsRune(cwd, '\x00') {
		return "", errors.New("Path contains null bytes")
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return filepath.Clean(cwd), nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("get user home directory: %w", err)
		}
		if strings.ContainsRune(home, '\x00') {
			return "", errors.New("Path contains null bytes")
		}
		if path == "~" {
			return filepath.Clean(home), nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(filepath.Join(cwd, path))
}

func attachmentStatError(rawPath, cwd string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("Attachment \"%s\" does not exist. Current working directory: %s.", rawPath, cwd)
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return fmt.Errorf("Attachment \"%s\" is not accessible (permission denied).", rawPath)
	default:
		return fmt.Errorf("stat attachment %q: %w", rawPath, err)
	}
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func uploadAttachments(ctx context.Context, attachments []Attachment, uploader Uploader) {
	if uploader == nil || len(attachments) == 0 {
		return
	}
	var group sync.WaitGroup
	group.Add(len(attachments))
	for index := range attachments {
		go func() {
			defer group.Done()
			attachment := attachments[index]
			uuid, err := uploader.Upload(ctx, attachment.Path, attachment.Size)
			if err == nil && uuid != "" {
				attachments[index].FileUUID = uuid
			}
		}()
	}
	group.Wait()
}
