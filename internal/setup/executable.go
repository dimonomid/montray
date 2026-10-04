package setup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExecutablePath returns this executable's symlink-resolved path.
func ExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	return path, nil
}

// PersistentExecutablePath rejects locations that cannot safely be retained
// by launchers or other installation artifacts after the current process exits.
func PersistentExecutablePath() (string, error) {
	path, err := ExecutablePath()
	if err != nil {
		return "", err
	}
	return requirePersistentExecutablePath(path, os.TempDir())
}

// requirePersistentExecutablePath keeps the location policy independently
// testable from the operating-system lookup of the current executable.
func requirePersistentExecutablePath(path, tempDir string) (string, error) {
	path = filepath.Clean(path)
	tempDir = filepath.Clean(tempDir)
	if path == tempDir || len(path) > len(tempDir) && path[:len(tempDir)+1] == tempDir+string(os.PathSeparator) {
		return "", fmt.Errorf("refusing to install from temporary executable %s", path)
	}
	return path, nil
}

// InstallExecutable atomically copies source to destination with permissions
// suitable for a system-wide executable. An existing destination is preserved
// unless reinstall is true. The returned value reports whether destination was
// created or replaced.
func InstallExecutable(source, destination string, reinstall bool) (bool, error) {
	if !reinstall {
		if _, err := os.Lstat(destination); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect executable %s: %w", destination, err)
		}
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return false, fmt.Errorf("open executable %s: %w", source, err)
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect executable %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("executable %s is not a regular file", source)
	}

	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return false, fmt.Errorf("create executable directory %s: %w", directory, err)
	}
	temporaryFile, err := os.CreateTemp(directory, "."+filepath.Base(destination)+".tmp-")
	if err != nil {
		return false, fmt.Errorf("create temporary executable: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	closeAttempted := false
	defer func() {
		if !closeAttempted {
			temporaryFile.Close()
		}
		os.Remove(temporaryPath)
	}()

	if err := temporaryFile.Chmod(0755); err != nil {
		return false, fmt.Errorf("set executable permissions: %w", err)
	}
	if _, err := io.Copy(temporaryFile, sourceFile); err != nil {
		return false, fmt.Errorf("copy executable: %w", err)
	}
	closeAttempted = true
	if err := temporaryFile.Close(); err != nil {
		return false, fmt.Errorf("close executable: %w", err)
	}
	if reinstall {
		if err := os.Rename(temporaryPath, destination); err != nil {
			return false, fmt.Errorf("install executable at %s: %w", destination, err)
		}
		return true, nil
	}
	if err := os.Link(temporaryPath, destination); os.IsExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("install executable at %s: %w", destination, err)
	}
	return true, nil
}
