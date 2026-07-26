package bash

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func resolveExecutable(name, workingDirectory string, environment []string) (string, error) {
	if name == "" {
		return "", exec.ErrNotFound
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDirectory, path)
		}
		var lastErr error = fs.ErrNotExist
		for _, candidate := range executableCandidates(path, environment) {
			ok, err := executableStatus(candidate)
			if ok {
				return candidate, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				lastErr = err
			}
		}
		return "", &os.PathError{Op: "fork/exec", Path: path, Err: lastErr}
	}
	var deniedPath string
	for _, pathDirectory := range filepath.SplitList(environmentValue(environment, "PATH")) {
		if pathDirectory == "" {
			pathDirectory = workingDirectory
		} else if !filepath.IsAbs(pathDirectory) {
			pathDirectory = filepath.Join(workingDirectory, pathDirectory)
		}
		for _, candidate := range executableCandidates(filepath.Join(pathDirectory, name), environment) {
			ok, err := executableStatus(candidate)
			if ok {
				return candidate, nil
			}
			if errors.Is(err, fs.ErrPermission) && deniedPath == "" {
				deniedPath = candidate
			}
		}
	}
	if deniedPath != "" {
		return "", &os.PathError{Op: "fork/exec", Path: deniedPath, Err: fs.ErrPermission}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if runtime.GOOS == "windows" {
			parts := strings.SplitN(environment[index], "=", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], name) {
				return parts[1]
			}
		} else if after, ok := strings.CutPrefix(environment[index], prefix); ok {
			return after
		}
	}
	return ""
}

func executableCandidates(path string, environment []string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(path) != "" {
		return []string{path}
	}
	extensions := environmentValue(environment, "PATHEXT")
	if extensions == "" {
		extensions = ".COM;.EXE;.BAT;.CMD"
	}
	parts := strings.Split(extensions, ";")
	result := make([]string, 0, len(parts)*2+1)
	result = append(result, path)
	for _, extension := range parts {
		if extension != "" {
			result = append(result, path+strings.ToLower(extension), path+strings.ToUpper(extension))
		}
	}
	return result
}

func executableStatus(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false, fs.ErrPermission
	}
	return true, nil
}
