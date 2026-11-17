package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/InkSoftware/cloudflare-ddns/internal/fsutil"
)

// writeHealthFile stores an expiry the way the daemon does, so the tests do not
// depend on how it is formatted.
func writeHealthFile(t *testing.T, path string, expiry int64) {
	t.Helper()
	if err := fsutil.WriteFileAtomic(path, []byte(strconv.FormatInt(expiry, 10)+"\n")); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "healthy")

	if err := Healthcheck(path); err == nil {
		t.Error("missing file: want error")
	}

	writeHealthFile(t, path, time.Now().Unix()+600)
	if err := Healthcheck(path); err != nil {
		t.Errorf("future expiry: %v", err)
	}

	writeHealthFile(t, path, time.Now().Unix()-1)
	if err := Healthcheck(path); err == nil {
		t.Error("past expiry: want error")
	}
}

func TestHealthcheckRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("9999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "healthy")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Healthcheck(link); err == nil {
		t.Error("symlinked health file: want error")
	}
}

func TestRetryDelay(t *testing.T) {
	hour := time.Hour
	cases := []struct {
		interval time.Duration
		failures int
		want     time.Duration
	}{
		{hour, 1, 30 * time.Second},
		{hour, 2, time.Minute},
		{hour, 3, 2 * time.Minute},
		{hour, 7, 32 * time.Minute},
		{hour, 8, hour},
		{hour, 1000, hour},
		{10 * time.Second, 1, 10 * time.Second},
	}
	for _, c := range cases {
		if got := retryDelay(c.interval, c.failures); got != c.want {
			t.Errorf("retryDelay(%v, %d) = %v, want %v", c.interval, c.failures, got, c.want)
		}
	}
}
