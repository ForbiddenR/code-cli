//go:build windows

package globalconfig

import (
	"io"
	"os"
)

func readMatchingRegularFile(path string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if err := validateOpenedFile(path, expected, file); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}
