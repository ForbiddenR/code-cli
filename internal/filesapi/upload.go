package filesapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// UploadResult describes the outcome of a single file upload.
type UploadResult struct {
	Path    string
	FileID  string
	Size    int64
	Success bool
	Error   string
}

// UploadFile uploads one file to the Files API using multipart/form-data.
func (c *Client) UploadFile(ctx context.Context, filePath string, relativePath string) UploadResult {
	result := UploadResult{Path: relativePath}
	content, err := os.ReadFile(filePath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(content) > MaxFileSizeBytes {
		result.Error = fmt.Sprintf("file exceeds maximum size of %d bytes (actual: %d)", MaxFileSizeBytes, len(content))
		return result
	}

	var fileID string
	err = c.retry(ctx, func(ctx context.Context) (bool, error) {
		body, contentType, err := buildUploadBody(content, relativePath)
		if err != nil {
			return false, err
		}
		headers := http.Header{"Content-Type": []string{contentType}, "Content-Length": []string{fmt.Sprint(body.Len())}}
		response, err := c.doWithHeadersTimeout(ctx, c.uploadTimeout, http.MethodPost, "/v1/files", nil, body, headers)
		if err != nil {
			return true, err
		}
		defer response.Body.Close()

		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
			var upload uploadResponse
			if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
				return false, fmt.Errorf("decode upload response: %w", err)
			}
			if upload.ID == "" {
				return true, fmt.Errorf("upload succeeded but no file id returned")
			}
			fileID = upload.ID
			return false, nil
		}
		if nonRetriableUploadStatus(response.StatusCode) {
			return false, fileAPIError(response)
		}
		return retryableStatus(response.StatusCode), fileAPIError(response)
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.FileID = fileID
	result.Size = int64(len(content))
	result.Success = true
	return result
}

func buildUploadBody(content []byte, relativePath string) (*bytes.Reader, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", filepath.Base(relativePath))
	if err != nil {
		return nil, "", fmt.Errorf("create file part: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", fmt.Errorf("write file part: %w", err)
	}
	if err := writer.WriteField("purpose", "user_data"); err != nil {
		return nil, "", fmt.Errorf("write purpose field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}
	return bytes.NewReader(buffer.Bytes()), writer.FormDataContentType(), nil
}

func nonRetriableUploadStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestEntityTooLarge:
		return true
	default:
		return false
	}
}

type uploadResponse struct {
	ID string `json:"id"`
}
