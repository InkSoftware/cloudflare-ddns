package main

import (
	"bytes"
	"testing"
)

func TestUnknownCommandIsUsageError(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"bogus"}, &bytes.Buffer{}, &stderr); code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if stderr.Len() == 0 {
		t.Error("want usage message on stderr")
	}
}

func TestOnceFailsWithoutConfig(t *testing.T) {
	t.Setenv("CF_TOKEN", "")
	t.Setenv("CF_ZONE", "")
	t.Setenv("CF_RECORD", "")

	var stderr bytes.Buffer
	if code := run([]string{"once"}, &bytes.Buffer{}, &stderr); code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}
	if got := stderr.String(); got != "CF_TOKEN is required\n" {
		t.Errorf("stderr = %q", got)
	}
}
