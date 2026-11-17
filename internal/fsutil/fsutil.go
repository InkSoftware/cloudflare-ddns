// Package fsutil holds the filesystem helpers the updater uses for its cache.
// They refuse symlinks and foreign-owned directories, so another local user
// cannot redirect reads or writes.
package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// PrepareCacheDir creates dir if needed and checks that it is safe to use as a
// cache. It must be a real directory, not a symlink, owned by the current user.
// Its permissions are set to 0700.
func PrepareCacheDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Lstat, not Stat, so a symlink at dir is seen as a symlink and rejected.
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("cache dir %s is not a plain directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("cache dir %s is owned by another user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// WriteFileAtomic replaces path with data so readers see either the old or the
// new contents, never a partial write. It writes a temp file in the same
// directory and renames it over path, which is atomic on POSIX filesystems.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	// Some filesystems report write errors only at close, so check both.
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// maxRead caps how much ReadRegularFile reads. Every file it is used for holds
// a single short line.
const maxRead = 4096

// ReadRegularFile reads path, but returns an error if path is a symlink, is
// not a regular file, is owned by another user, or is larger than maxRead.
// The checks are made on the opened file, so the path cannot be swapped for a
// symlink between the check and the read. O_NONBLOCK keeps a FIFO planted at
// path from blocking the open.
func ReadRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("%s is owned by another user", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRead {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return data, nil
}

// ReadTrimmed returns the trimmed contents of the regular file at path, or ""
// if it is missing, is not a regular file, or cannot be read.
func ReadTrimmed(path string) string {
	data, err := ReadRegularFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// IsOlderThan reports whether the file at path was last modified more than age
// ago. It uses Lstat, so symlinks are not followed. A missing or non-regular
// file is reported as not older than age.
func IsOlderThan(path string, age time.Duration) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return time.Since(info.ModTime()) > age
}
