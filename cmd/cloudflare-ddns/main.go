// Command cloudflare-ddns keeps Cloudflare DNS A/AAAA records pointed at this
// host's current public IP address.
//
// Usage:
//
//	cloudflare-ddns              run forever, updating every DDNS_INTERVAL seconds
//	cloudflare-ddns once         run a single update cycle and exit
//	cloudflare-ddns healthcheck  exit 0 while the last successful cycle is fresh
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/InkSoftware/cloudflare-ddns/internal/app"
	"github.com/InkSoftware/cloudflare-ddns/internal/config"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1 // Config, update, or health failure.
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command named by args and returns its exit code. It takes
// its output streams as parameters so tests can capture them.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "":
		cfg, ok := loadConfig(stderr)
		if !ok {
			return exitError
		}
		ctx, stop := signalContext()
		defer stop()
		app.Daemon(ctx, cfg, stdout, stderr)
		return exitOK

	case "once":
		cfg, ok := loadConfig(stderr)
		if !ok {
			return exitError
		}
		ctx, stop := signalContext()
		defer stop()
		if err := app.Once(ctx, cfg, stdout); err != nil {
			return exitError
		}
		return exitOK

	case "healthcheck":
		// Does not load the full config, so it works even if the update
		// settings are incomplete.
		if err := app.Healthcheck(config.HealthFile(os.Getenv)); err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
		return exitOK

	default:
		fmt.Fprintf(stderr, "usage: cloudflare-ddns [once|healthcheck]\n")
		return exitUsage
	}
}

// loadConfig reads the configuration from the environment. On failure it
// writes the error to errOut and returns ok=false.
func loadConfig(errOut io.Writer) (config.Config, bool) {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return config.Config{}, false
	}
	return cfg, true
}

// signalContext returns a context cancelled by SIGTERM or SIGINT, so a stop
// request ends the sleep or the in-flight HTTP call promptly.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}
