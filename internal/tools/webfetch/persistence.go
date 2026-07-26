package webfetch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type DirectoryPersister struct {
	directory string
	now       func() time.Time
	sequence  atomic.Uint64
}

func NewDirectoryPersister(directory string, now func() time.Time) *DirectoryPersister {
	if now == nil {
		now = time.Now
	}
	return &DirectoryPersister{directory: directory, now: now}
}

func (p *DirectoryPersister) Persist(ctx context.Context, suggestedName, _ string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p == nil || p.directory == "" {
		return "", fmt.Errorf("binary output directory is empty")
	}
	if err := os.MkdirAll(p.directory, 0o700); err != nil {
		return "", fmt.Errorf("create binary output directory: %w", err)
	}
	extension := filepath.Ext(filepath.Base(suggestedName))
	if extension == "" || strings.ContainsAny(extension, `/\\`) {
		extension = ".bin"
	}
	filename := fmt.Sprintf("webfetch-%d-%06d%s", p.now().UnixMilli(), p.sequence.Add(1), extension)
	path := filepath.Join(p.directory, filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("persist binary content: %w", err)
	}
	return path, nil
}
