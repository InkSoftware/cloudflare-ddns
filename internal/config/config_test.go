package config

import (
	"strings"
	"testing"
	"time"
)

// env returns a getenv function backed by m.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// base is the smallest environment that loads successfully.
func base() map[string]string {
	return map[string]string{
		"CF_TOKEN":  "tok",
		"CF_ZONE":   "example.com",
		"CF_RECORD": "home.example.com",
	}
}

func withEnv(kv ...string) map[string]string {
	m := base()
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestDefaults(t *testing.T) {
	cfg, err := load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"Token", cfg.Token, "tok"},
		{"Zone", cfg.Zone, "example.com"},
		{"Record", cfg.Record, "home.example.com"},
		{"ZoneID", cfg.ZoneID, ""},
		{"TTL", cfg.TTL, 1},
		{"Proxied", cfg.Proxied, false},
		{"IPv4", cfg.IPv4, true},
		{"IPv6", cfg.IPv6, true},
		{"CreateMissing", cfg.CreateMissing, true},
		{"Force", cfg.Force, false},
		{"Interval", cfg.Interval, 3600},
		{"CacheDir", cfg.CacheDir, "/tmp/cf-ddns"},
		{"CacheMaxAge", cfg.CacheMaxAge, 1440 * time.Minute},
		{"HealthFile", cfg.HealthFile, "/tmp/healthy"},
		{"APIBase", cfg.APIBase, "https://api.cloudflare.com/client/v4"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if cfg.IPv4URL == "" || cfg.IPv6URL == "" {
		t.Error("default IP echo URLs are empty")
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := load(env(withEnv(
		"CF_ZONE_ID", "z1",
		"CF_TTL", "300",
		"CF_PROXIED", "1",
		"CF_IPV6", "false",
		"CF_CREATE_MISSING", "0",
		"FORCE", "1",
		"DDNS_INTERVAL", "60",
		"CACHE_DIR", "/var/cache/ddns",
		"CACHE_MAX_AGE", "0",
		"HEALTH_FILE", "/run/ddns/healthy",
	)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZoneID != "z1" || cfg.TTL != 300 || !cfg.Proxied || cfg.IPv6 ||
		cfg.CreateMissing || !cfg.Force || cfg.Interval != 60 ||
		cfg.CacheDir != "/var/cache/ddns" || cfg.CacheMaxAge != 0 ||
		cfg.HealthFile != "/run/ddns/healthy" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestRequiredVariables(t *testing.T) {
	for _, key := range []string{"CF_TOKEN", "CF_ZONE", "CF_RECORD"} {
		m := base()
		m[key] = "   " // Whitespace counts as empty.
		_, err := load(env(m))
		if err == nil || err.Error() != key+" is required" {
			t.Errorf("%s unset: err = %v", key, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		kv   []string
		want string // Substring the error must contain.
	}{
		{"interval zero", []string{"DDNS_INTERVAL", "0"}, "DDNS_INTERVAL"},
		{"interval text", []string{"DDNS_INTERVAL", "often"}, "DDNS_INTERVAL"},
		{"ttl too small", []string{"CF_TTL", "30"}, "CF_TTL"},
		{"ttl too large", []string{"CF_TTL", "86401"}, "CF_TTL"},
		{"bool junk", []string{"CF_PROXIED", "yes please"}, "CF_PROXIED"},
		{"negative cache age", []string{"CACHE_MAX_AGE", "-5"}, "CACHE_MAX_AGE"},
		{"interval overflow", []string{"DDNS_INTERVAL", "10000000000"}, "DDNS_INTERVAL"},
		{"cache age overflow", []string{"CACHE_MAX_AGE", "200000000000"}, "CACHE_MAX_AGE"},
		{"no family enabled", []string{"CF_IPV4", "false", "CF_IPV6", "false"}, "CF_IPV4 and CF_IPV6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(env(withEnv(c.kv...)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestRecordIsNormalized(t *testing.T) {
	cfg, err := load(env(withEnv("CF_ZONE", "Example.COM.", "CF_RECORD", " Home.Example.com. ")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Zone != "example.com" || cfg.Record != "home.example.com" {
		t.Errorf("zone=%q record=%q, want example.com and home.example.com", cfg.Zone, cfg.Record)
	}
}

func TestRecordMustBeInZone(t *testing.T) {
	for _, rec := range []string{"example.com", "a.b.example.com"} {
		if _, err := load(env(withEnv("CF_RECORD", rec))); err != nil {
			t.Errorf("CF_RECORD=%s: %v", rec, err)
		}
	}
	for _, rec := range []string{"home", "home.example.org", "notexample.com"} {
		_, err := load(env(withEnv("CF_RECORD", rec)))
		if err == nil || !strings.Contains(err.Error(), "not in zone") {
			t.Errorf("CF_RECORD=%s: err = %v, want not-in-zone error", rec, err)
		}
	}
}

func TestInvalidValuesAreAllReported(t *testing.T) {
	_, err := load(env(withEnv("DDNS_INTERVAL", "x", "CF_TTL", "2")))
	if err == nil || !strings.Contains(err.Error(), "DDNS_INTERVAL") || !strings.Contains(err.Error(), "CF_TTL") {
		t.Errorf("err = %v, want both problems reported", err)
	}
}

func TestTTLAcceptsCloudflareRange(t *testing.T) {
	for _, v := range []string{"60", "86400"} {
		if _, err := load(env(withEnv("CF_TTL", v))); err != nil {
			t.Errorf("CF_TTL=%s: %v", v, err)
		}
	}
}

func TestHealthFile(t *testing.T) {
	if got := HealthFile(env(nil)); got != "/tmp/healthy" {
		t.Errorf("default = %q", got)
	}
	if got := HealthFile(env(map[string]string{"HEALTH_FILE": " /x "})); got != "/x" {
		t.Errorf("override = %q", got)
	}
}

// HealthFile must work when the rest of the configuration is broken, because
// the container healthcheck runs without the update settings.
func TestHealthFileIgnoresOtherSettings(t *testing.T) {
	if got := HealthFile(env(map[string]string{"DDNS_INTERVAL": "bad"})); got != "/tmp/healthy" {
		t.Errorf("got %q", got)
	}
}
