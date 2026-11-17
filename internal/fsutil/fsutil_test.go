package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomicDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "zone_example.com")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(link, []byte("new\n")); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("symlink target was written: %q", got)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("link should now be a regular file (err %v)", err)
	}
	if got, _ := os.ReadFile(link); string(got) != "new\n" {
		t.Errorf("link contents = %q", got)
	}
}

func TestReadRegularFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "record.A")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadRegularFile(link); err == nil {
		t.Error("symlink read: want error")
	}
	if got := ReadTrimmed(link); got != "" {
		t.Errorf("ReadTrimmed(symlink) = %q, want empty", got)
	}
	if got := ReadTrimmed(target); got != "1.2.3.4" {
		t.Errorf("ReadTrimmed(regular) = %q", got)
	}
}

func TestReadRegularFileRejectsFIFOAndLargeFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(fifo); err == nil {
		t.Error("FIFO read: want error")
	}

	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, make([]byte, maxRead+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(big); err == nil {
		t.Error("oversized read: want error")
	}
}

func TestPrepareCacheDir(t *testing.T) {
	base := t.TempDir()

	fresh := filepath.Join(base, "fresh")
	if err := PrepareCacheDir(fresh); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(fresh); info.Mode().Perm() != 0o700 {
		t.Errorf("new dir mode = %o, want 700", info.Mode().Perm())
	}

	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCacheDir(loose); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(loose); info.Mode().Perm() != 0o700 {
		t.Errorf("existing dir mode = %o, want 700", info.Mode().Perm())
	}

	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(base, "sym")
	if err := os.Symlink(realDir, symDir); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCacheDir(symDir); err == nil {
		t.Error("symlinked cache dir: want error")
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCacheDir(file); err == nil {
		t.Error("cache path is a file: want error")
	}
}
