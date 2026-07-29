//go:build !windows

package globalconfig

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readMatchingRegularFile(path string, expected os.FileInfo) ([]byte, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %q changed while opening", ErrUnsafePath, path)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open global configuration file handle")
	}
	defer file.Close()

	if err := validateOpenedFile(path, expected, file); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}
