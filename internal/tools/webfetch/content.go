package webfetch

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

type defaultHTMLConverter struct{}

func (defaultHTMLConverter) Convert(input string) (string, error) {
	return htmltomarkdown.ConvertString(input)
}

func isBinaryContentType(contentType string) bool {
	mediaType := normalizedContentType(contentType)
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		return false
	}
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		return false
	}
	if mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") {
		return false
	}
	if strings.HasPrefix(mediaType, "application/javascript") || mediaType == "application/x-www-form-urlencoded" {
		return false
	}
	return true
}

func extensionForContentType(contentType string) string {
	extensions := map[string]string{
		"application/pdf":  "pdf",
		"application/json": "json",
		"text/csv":         "csv",
		"text/plain":       "txt",
		"text/html":        "html",
		"text/markdown":    "md",
		"application/zip":  "zip",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
		"application/msword":       "doc",
		"application/vnd.ms-excel": "xls",
		"audio/mpeg":               "mp3",
		"audio/wav":                "wav",
		"audio/ogg":                "ogg",
		"video/mp4":                "mp4",
		"video/webm":               "webm",
		"image/png":                "png",
		"image/jpeg":               "jpg",
		"image/gif":                "gif",
		"image/webp":               "webp",
		"image/svg+xml":            "svg",
	}
	if extension, ok := extensions[normalizedContentType(contentType)]; ok {
		return extension
	}
	return "bin"
}

func normalizedContentType(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func formatFileSize(size int) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d bytes", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		value /= unit
		if value < unit || suffix == units[len(units)-1] {
			rounded := math.Round(value*10) / 10
			return strconv.FormatFloat(rounded, 'f', -1, 64) + suffix
		}
	}
	return fmt.Sprintf("%d bytes", size)
}
