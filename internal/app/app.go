// Package app implements the program's commands: the long-running daemon, a
// single update, and the health check that container orchestrators call.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/InkSoftware/cloudflare-ddns/internal/config"
	"github.com/InkSoftware/cloudflare-ddns/internal/fsutil"
	"github.com/InkSoftware/cloudflare-ddns/internal/updater"
)

// healthGrace is the slack, in seconds, added to the update interval when
// computing the health expiry. It absorbs slow cycles so one slow run does
// not make the container look unhealthy.
const healthGrace = 300

// Once runs a single update cycle.
func Once(ctx context.Context, cfg config.Config, out io.Writer) error {
	return updater.RunCycle(ctx, cfg, out)
}

// Daemon runs update cycles every cfg.Interval seconds until ctx is cancelled.
// A failed cycle is reported on errOut and retried sooner, with exponential
// backoff (see retryDelay). The health file is refreshed only after a
// successful cycle, so a daemon that keeps failing eventually fails the
// healthcheck.
func Daemon(ctx context.Context, cfg config.Config, out, errOut io.Writer) {
	fmt.Fprintln(out, "DDNS Agent")
	// Remove a health file left by an earlier run, so the container is not
	// reported healthy before this process has completed a good cycle.
	_ = os.Remove(cfg.HealthFile)

	interval := time.Duration(cfg.Interval) * time.Second
	failures := 0
	for ctx.Err() == nil {
		err := updater.RunCycle(ctx, cfg, out)
		if ctx.Err() != nil {
			return // Stopping; a cycle cut short is not a failure.
		}
		delay := interval
		if err == nil {
			failures = 0
			expiry := time.Now().Unix() + int64(cfg.Interval) + healthGrace
			writeHealth(cfg.HealthFile, expiry, errOut)
		} else {
			failures++
			delay = retryDelay(interval, failures)
			fmt.Fprintln(errOut, "DDNS update failed")
		}

		fmt.Fprintf(out, "Sleeping for %d\n", int(delay/time.Second))
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}
}

// retryBase is the wait after the first failed cycle.
const retryBase = 30 * time.Second

// retryDelay returns the wait after the given number of consecutive failed
// cycles (at least 1): retryBase, doubled for each further failure, and never
// more than interval.
func retryDelay(interval time.Duration, failures int) time.Duration {
	d := retryBase
	for i := 1; i < failures && d < interval; i++ {
		d *= 2
	}
	return min(d, interval)
}

// Healthcheck returns nil if the health file at path holds an expiry time in
// the future. Otherwise it returns an error saying why.
func Healthcheck(path string) error {
	data, err := fsutil.ReadRegularFile(path)
	if err != nil {
		return fmt.Errorf("health file: %w", err)
	}
	expiry, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return fmt.Errorf("health file is malformed: %w", err)
	}
	if time.Now().Unix() >= expiry {
		return errors.New("last successful update has expired")
	}
	return nil
}

// writeHealth records the Unix time after which the daemon counts as unhealthy.
// A write error is only logged. A missing health file makes the healthcheck
// fail, which is the safe direction.
func writeHealth(path string, expiry int64, errOut io.Writer) {
	if err := fsutil.WriteFileAtomic(path, []byte(strconv.FormatInt(expiry, 10)+"\n")); err != nil {
		fmt.Fprintf(errOut, "❌ cannot write %s: %v\n", path, err)
	}
}
