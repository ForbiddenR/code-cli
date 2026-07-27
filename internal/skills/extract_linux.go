//go:build linux

package skills

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"syscall"
)

type bundledExtractor struct {
	once  sync.Once
	root  string
	files map[string]string
	err   error
}

func (extractor *bundledExtractor) extract() (string, error) {
	if extractor == nil {
		return "", errors.New("bundled skill extractor is nil")
	}
	extractor.once.Do(func() {
		extractor.err = extractBundledFiles(extractor.root, extractor.files)
	})
	if extractor.err != nil {
		return "", extractor.err
	}
	return extractor.root, nil
}

func extractBundledFiles(root string, files map[string]string) error {
	for name := range files {
		if err := validateBundledFilePath(name); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create bundled skill root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("protect bundled skill root: %w", err)
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open bundled skill root: %w", err)
	}
	defer syscall.Close(rootFD)
	for _, name := range sortedBundledFileNames(files) {
		if err := writeBundledFileAt(rootFD, name, files[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateBundledFilePath(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("%w: %q", ErrInvalidBundledFile, name)
	}
	for component := range strings.SplitSeq(name, "/") {
		if component == "" || component == "." || component == ".." || strings.ContainsRune(component, '\x00') {
			return fmt.Errorf("%w: %q", ErrInvalidBundledFile, name)
		}
	}
	return nil
}

func writeBundledFileAt(rootFD int, name, content string) error {
	components := strings.Split(name, "/")
	currentFD := rootFD
	ownedFD := -1
	defer func() {
		if ownedFD >= 0 {
			syscall.Close(ownedFD)
		}
	}()
	for _, component := range components[:len(components)-1] {
		if err := syscall.Mkdirat(currentFD, component, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("create bundled skill directory %q: %w", component, err)
		}
		nextFD, err := syscall.Openat(currentFD, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open bundled skill directory %q: %w", component, err)
		}
		if ownedFD >= 0 {
			syscall.Close(ownedFD)
		}
		ownedFD = nextFD
		currentFD = nextFD
	}
	fileName := components[len(components)-1]
	fileFD, err := syscall.Openat(currentFD, fileName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create bundled skill file %q: %w", name, err)
	}
	file := os.NewFile(uintptr(fileFD), fileName)
	if file == nil {
		syscall.Close(fileFD)
		return fmt.Errorf("create bundled skill file handle %q", name)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return fmt.Errorf("write bundled skill file %q: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close bundled skill file %q: %w", name, err)
	}
	return nil
}
