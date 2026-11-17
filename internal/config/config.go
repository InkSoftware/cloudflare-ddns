// Package config loads and validates the program's settings from environment
// variables. Every setting has a default except the three Cloudflare values
// (CF_TOKEN, CF_ZONE and CF_RECORD), which must be set.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for settings that are optional in the environment.
const (
	defaultInterval    = 3600
	defaultTTL         = 1
	defaultCacheDir    = "/tmp/cf-ddns"
	defaultCacheMaxAge = 1440 // Minutes.
	defaultHealthFile  = "/tmp/healthy"

	// Upper bounds. Larger values would overflow time.Duration, which turns
	// the sleep negative and makes the daemon call Cloudflare in a tight loop.
	maxInterval    = 7 * 24 * 3600 // One week, in seconds.
	maxCacheMaxAge = 365 * 24 * 60 // One year, in minutes.
	defaultAPIBase = "https://api.cloudflare.com/client/v4"
)

// Public IP echo services, returning the caller's address as plain text.
// icanhazip is run by Cloudflare, so it adds no third party to trust.
const (
	defaultIPv4URL = "https://ipv4.icanhazip.com"
	defaultIPv6URL = "https://ipv6.icanhazip.com"
)

// Config holds the validated settings for one run.
type Config struct {
	Token         string        // CF_TOKEN: API token with Zone:DNS:Edit.
	Zone          string        // CF_ZONE: zone name, e.g. example.com.
	ZoneID        string        // CF_ZONE_ID: optional, skips the zone lookup.
	Record        string        // CF_RECORD: record name, e.g. home.example.com.
	TTL           int           // CF_TTL: 1 for automatic, otherwise 60 to 86400.
	Proxied       bool          // CF_PROXIED: route through Cloudflare's proxy.
	IPv4          bool          // CF_IPV4: update the A record.
	IPv6          bool          // CF_IPV6: update the AAAA record.
	CreateMissing bool          // CF_CREATE_MISSING: create the record if absent.
	Force         bool          // FORCE: ignore the cache for this run.
	Interval      int           // DDNS_INTERVAL: seconds between runs, positive.
	CacheDir      string        // CACHE_DIR: directory for cache files.
	CacheMaxAge   time.Duration // CACHE_MAX_AGE: minutes; 0 means never stale.
	HealthFile    string        // HEALTH_FILE: path of the health timestamp.
	APIBase       string        // Cloudflare API base URL, without a trailing slash.
	IPv4URL       string        // IP echo service for IPv4.
	IPv6URL       string        // IP echo service for IPv6.
}

// FromEnv reads the configuration from the process environment. It returns an
// error naming the first setting that is missing or invalid.
func FromEnv() (Config, error) {
	return load(os.Getenv)
}

// HealthFile returns the health file path from HEALTH_FILE, or its default.
// It takes the lookup function as a parameter so the health check can find
// the path without validating the rest of the configuration.
func HealthFile(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("HEALTH_FILE")); v != "" {
		return v
	}
	return defaultHealthFile
}

// load builds a Config from getenv. It is separate from FromEnv so tests can
// supply their own environment.
func load(getenv func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Token:      strings.TrimSpace(getenv("CF_TOKEN")),
		Zone:       dnsName(getenv("CF_ZONE")),
		Record:     dnsName(getenv("CF_RECORD")),
		ZoneID:     strings.TrimSpace(getenv("CF_ZONE_ID")),
		CacheDir:   str("CACHE_DIR", defaultCacheDir),
		HealthFile: HealthFile(getenv),
		APIBase:    defaultAPIBase,
		IPv4URL:    defaultIPv4URL,
		IPv6URL:    defaultIPv6URL,
	}

	for _, req := range []struct{ key, val string }{
		{"CF_TOKEN", cfg.Token},
		{"CF_ZONE", cfg.Zone},
		{"CF_RECORD", cfg.Record},
	} {
		if req.val == "" {
			return Config{}, fmt.Errorf("%s is required", req.key)
		}
	}

	if cfg.Record != cfg.Zone && !strings.HasSuffix(cfg.Record, "."+cfg.Zone) {
		return Config{}, fmt.Errorf("CF_RECORD %q is not in zone %q; use the full name, e.g. home.%s", cfg.Record, cfg.Zone, cfg.Zone)
	}

	var errs []error
	var err error

	if cfg.TTL, err = parseTTL(str("CF_TTL", strconv.Itoa(defaultTTL))); err != nil {
		errs = append(errs, err)
	}
	if cfg.Interval, err = parseInt("DDNS_INTERVAL", str("DDNS_INTERVAL", strconv.Itoa(defaultInterval)), 1, maxInterval); err != nil {
		errs = append(errs, err)
	}
	if cfg.CacheMaxAge, err = parseCacheMaxAge(str("CACHE_MAX_AGE", strconv.Itoa(defaultCacheMaxAge))); err != nil {
		errs = append(errs, err)
	}
	if cfg.Proxied, err = parseBool("CF_PROXIED", str("CF_PROXIED", "false")); err != nil {
		errs = append(errs, err)
	}
	if cfg.IPv4, err = parseBool("CF_IPV4", str("CF_IPV4", "true")); err != nil {
		errs = append(errs, err)
	}
	if cfg.IPv6, err = parseBool("CF_IPV6", str("CF_IPV6", "true")); err != nil {
		errs = append(errs, err)
	}
	if cfg.CreateMissing, err = parseBool("CF_CREATE_MISSING", str("CF_CREATE_MISSING", "true")); err != nil {
		errs = append(errs, err)
	}
	if cfg.Force, err = parseBool("FORCE", str("FORCE", "false")); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}

	if !cfg.IPv4 && !cfg.IPv6 {
		return Config{}, errors.New("CF_IPV4 and CF_IPV6 are both false; enable at least one")
	}
	return cfg, nil
}

// dnsName trims s and returns it in the form Cloudflare stores names in:
// lowercase, without a trailing dot.
func dnsName(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// parseBool accepts the forms strconv.ParseBool does, such as true, false, 1
// and 0.
func parseBool(key, s string) (bool, error) {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false (or 1 or 0), got %q", key, s)
	}
	return v, nil
}

// parseInt parses a whole number from lo to hi.
func parseInt(key, s string, lo, hi int) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s must be an integer from %d to %d, got %q", key, lo, hi, s)
	}
	return v, nil
}

// parseTTL accepts 1 (automatic) or a TTL from 60 to 86400 seconds, which are
// the values Cloudflare allows.
func parseTTL(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || (v != 1 && (v < 60 || v > 86400)) {
		return 0, fmt.Errorf("CF_TTL must be 1 (automatic) or 60 to 86400, got %q", s)
	}
	return v, nil
}

// parseCacheMaxAge parses a number of minutes. Zero means cached entries never
// expire.
func parseCacheMaxAge(s string) (time.Duration, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 || v > maxCacheMaxAge {
		return 0, fmt.Errorf("CACHE_MAX_AGE must be 0 to %d minutes, got %q", maxCacheMaxAge, s)
	}
	return time.Duration(v) * time.Minute, nil
}
