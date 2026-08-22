// Package securefs confines each configured file operation to the file's
// resolved parent directory. It prevents a filename or symlink from escaping
// that directory while still allowing users to choose an explicit config/data
// directory.
package securefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func split(path string) (string, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", "", errors.New("path must identify a file")
	}
	return filepath.Dir(abs), base, nil
}

func ReadFile(path string) ([]byte, error) {
	dir, base, err := split(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(base)
}

func WriteFile(path string, data []byte, mode fs.FileMode) error {
	dir, base, err := split(path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.WriteFile(base, data, mode); err != nil {
		return err
	}
	return root.Chmod(base, mode)
}

// WriteFileExclusive creates a new regular file without following an existing
// symlink or replacing an existing path. It is intended for update staging and
// other operations where a predictable overwrite would create a TOCTOU risk.
func WriteFileExclusive(path string, data []byte, mode fs.FileMode) error {
	dir, base, err := split(path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(base, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = root.Remove(base)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(base)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(base)
		return err
	}
	return root.Chmod(base, mode)
}

func Remove(path string) error {
	dir, base, err := split(path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(base)
}

func Stat(path string) (fs.FileInfo, error) {
	dir, base, err := split(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(base)
}

func Rename(oldPath, newPath string) error {
	oldDir, oldBase, err := split(oldPath)
	if err != nil {
		return err
	}
	newDir, newBase, err := split(newPath)
	if err != nil {
		return err
	}
	if oldDir != newDir {
		return errors.New("secure rename requires both files in the same directory")
	}
	root, err := os.OpenRoot(oldDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Rename(oldBase, newBase)
}
