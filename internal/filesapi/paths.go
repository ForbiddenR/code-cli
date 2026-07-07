package filesapi

import (
	"path/filepath"
	"strings"
)

// BuildDownloadPath builds the full download path under {basePath}/{sessionID}/uploads.
func BuildDownloadPath(basePath string, sessionID string, relativePath string) (string, bool) {
	normalized := filepath.Clean(relativePath)
	if strings.HasPrefix(normalized, "..") {
		return "", false
	}

	uploadsBase := filepath.Join(basePath, sessionID, "uploads")
	prefixes := []string{
		uploadsBase + string(filepath.Separator),
		string(filepath.Separator) + "uploads" + string(filepath.Separator),
	}
	cleanPath := normalized
	for _, prefix := range prefixes {
		if after, ok := strings.CutPrefix(cleanPath, prefix); ok {
			cleanPath = after
			break
		}
	}
	return filepath.Join(uploadsBase, cleanPath), true
}

// ParseFileSpecs parses file attachment specs in file_id:relative_path form.
func ParseFileSpecs(fileSpecs []string) []File {
	files := make([]File, 0, len(fileSpecs))
	for _, specGroup := range fileSpecs {
		for spec := range strings.FieldsSeq(specGroup) {
			fileID, relativePath, ok := strings.Cut(spec, ":")
			if !ok || fileID == "" || relativePath == "" {
				continue
			}
			files = append(files, File{FileID: fileID, RelativePath: relativePath})
		}
	}
	return files
}
