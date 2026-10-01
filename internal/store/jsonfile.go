package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrInvalidState marks persisted state that exists but cannot be trusted.
// Callers must surface it rather than discard the file.
var ErrInvalidState = errors.New("invalid persisted state")

// WriteJSONAtomic writes v as indented JSON with a trailing newline. The data
// goes to a fresh temporary file in the target directory, is synced, and is
// renamed over path, so readers see either the old or the new content.
func WriteJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// WriteFileAtomic writes data to path through a synced temporary file and a
// rename. Missing parent directories are created.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadJSON decodes path into v. It reports found=false for a missing file and
// wraps ErrInvalidState for content that is not a single JSON value of the
// expected shape. Unknown fields are tolerated so files written by newer or
// upstream producers still load.
func ReadJSON(path string, v any) (found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return true, fmt.Errorf("%w: %s: %v", ErrInvalidState, path, err)
	}
	if dec.More() {
		return true, fmt.Errorf("%w: %s: trailing data", ErrInvalidState, path)
	}
	return true, nil
}

// RemoveIfExists deletes path, treating an already-missing file as success.
func RemoveIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func invalid(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidState, path, fmt.Sprintf(format, args...))
}
