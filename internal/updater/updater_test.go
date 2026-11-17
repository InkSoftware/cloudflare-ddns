package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InkSoftware/cloudflare-ddns/internal/config"
	"github.com/InkSoftware/cloudflare-ddns/internal/fsutil"
)

const apiPrefix = "/client/v4"

// fakeCF stands in for the Cloudflare API. It records every write it receives.
type fakeCF struct {
	mu          sync.Mutex
	zone        string
	record      string
	write       string
	writes      []string
	zoneLookups int
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && path == "/zones":
		f.zoneLookups++
		fmt.Fprint(w, f.zone)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/zones/z1/dns_records"):
		fmt.Fprint(w, f.record)
	case r.Method == http.MethodPost || r.Method == http.MethodPatch:
		body, _ := io.ReadAll(r.Body)
		f.writes = append(f.writes, r.Method+" "+path+" "+string(body))
		fmt.Fprint(w, f.write)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCF) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *fakeCF) lastWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// ipServer stands in for the IP-echo services. The IPv4 and IPv6 endpoints
// share a handler and are told apart by how the client connected.
type ipServer struct {
	ipv4, ipv6 string
}

func (s *ipServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := s.ipv4
	if strings.HasPrefix(r.RemoteAddr, "[") {
		ip = s.ipv6
	}
	if ip == "" {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, ip)
}

const (
	testIPv4 = "1.2.3.4"
	testIPv6 = "2606:4700::1"
	zoneJSON = `{"success":true,"result":[{"id":"z1"}]}`
	emptyRec = `{"success":true,"result":[]}`
	okWrite  = `{"success":true}`
)

type harness struct {
	t    *testing.T
	cf   *fakeCF
	ip   *ipServer
	cfg  config.Config
	srvs []*httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:  t,
		cf: &fakeCF{zone: zoneJSON, record: emptyRec, write: okWrite},
		ip: &ipServer{ipv4: testIPv4, ipv6: testIPv6},
	}

	api := h.serve(h.cf, "tcp4", "127.0.0.1:0")
	v4 := h.serve(h.ip, "tcp4", "127.0.0.1:0")
	v6 := h.serve(h.ip, "tcp6", "[::1]:0")

	h.cfg = config.Config{
		Token:         "test-token",
		Zone:          "example.com",
		Record:        "home.example.com",
		TTL:           1,
		IPv4:          true,
		IPv6:          true,
		CreateMissing: true,
		CacheDir:      t.TempDir(),
		CacheMaxAge:   1440 * time.Minute,
		APIBase:       api.URL + apiPrefix,
		IPv4URL:       v4.URL,
		IPv6URL:       v6.URL,
	}
	t.Cleanup(func() {
		for _, s := range h.srvs {
			s.Close()
		}
	})
	return h
}

func (h *harness) serve(handler http.Handler, network, addr string) *httptest.Server {
	h.t.Helper()
	l, err := net.Listen(network, addr)
	if err != nil {
		h.t.Fatalf("listen %s %s: %v", network, addr, err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener.Close()
	s.Listener = l
	s.Start()
	h.srvs = append(h.srvs, s)
	return s
}

// run applies mutate to the config, runs one cycle, and returns the output and
// whether the cycle succeeded.
func (h *harness) run(mutate ...func(*config.Config)) (out string, ok bool) {
	h.t.Helper()
	for _, m := range mutate {
		m(&h.cfg)
	}
	var buf bytes.Buffer
	err := RunCycle(context.Background(), h.cfg, &buf)
	return buf.String(), err == nil
}

func (h *harness) cachePath(name string) string {
	return filepath.Join(h.cfg.CacheDir, name)
}

func contains(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q; got:\n%s", want, out)
	}
}

func noWrites(t *testing.T, h *harness) {
	t.Helper()
	if n := h.cf.writeCount(); n != 0 {
		t.Errorf("expected no Cloudflare writes, got %d: %v", n, h.cf.lastWrites())
	}
}

func onlyIPv4(c *config.Config) { c.IPv6 = false }

func TestCreatesMissingRecord(t *testing.T) {
	h := newHarness(t)
	out, ok := h.run(onlyIPv4)

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "Creating A record: "+testIPv4)
	contains(t, out, "A set to "+testIPv4)
	if w := h.cf.lastWrites(); len(w) != 1 || !strings.HasPrefix(w[0], "POST ") {
		t.Errorf("want one POST, got %v", w)
	}
	if got := fsutil.ReadTrimmed(h.cachePath("home.example.com.A")); !strings.HasPrefix(got, testIPv4+" ") {
		t.Errorf("cache = %q, want %q", got, testIPv4)
	}
}

// Regression: Cloudflare returns the created or updated record as a JSON
// object, not an array. Earlier code could not decode that and reported a
// successful write as rejected.
func TestWriteAcceptsObjectResult(t *testing.T) {
	h := newHarness(t)
	h.cf.write = `{"success":true,"errors":[],"messages":[],"result":{"id":"rec9","content":"` + testIPv4 + `"}}`
	out, ok := h.run(onlyIPv4)

	if !ok {
		t.Fatalf("successful write reported as failure:\n%s", out)
	}
	contains(t, out, "A set to "+testIPv4)
	if got := fsutil.ReadTrimmed(h.cachePath("home.example.com.A")); !strings.HasPrefix(got, testIPv4+" ") {
		t.Errorf("cache = %q, want %q", got, testIPv4)
	}
}

func TestWarmCacheSkipsCloudflare(t *testing.T) {
	h := newHarness(t)
	h.run(onlyIPv4)

	before := h.cf.writeCount()
	out, ok := h.run(onlyIPv4)

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "A "+testIPv4+" unchanged (cached)")
	if h.cf.writeCount() != before {
		t.Errorf("cached run wrote to Cloudflare")
	}
}

func TestMatchingRecordIsNotRewritten(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":true,"result":[{"id":"rec1","content":"` + testIPv4 + `","ttl":1}]}`
	out, ok := h.run(onlyIPv4)

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "A already "+testIPv4+" on Cloudflare")
	noWrites(t, h)
	if got := fsutil.ReadTrimmed(h.cachePath("home.example.com.A")); !strings.HasPrefix(got, testIPv4+" ") {
		t.Errorf("cache = %q, want %q", got, testIPv4)
	}
}

func TestStaleRecordIsUpdatedInPlace(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":true,"result":[{"id":"rec1","content":"198.51.100.1"}]}`
	out, ok := h.run(onlyIPv4)

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "Updating A: 198.51.100.1 → "+testIPv4)
	w := h.cf.lastWrites()
	if len(w) != 1 || !strings.HasPrefix(w[0], "PATCH /zones/z1/dns_records/rec1 ") {
		t.Fatalf("want one PATCH to rec1, got %v", w)
	}
	if !strings.Contains(w[0], `"content":"`+testIPv4+`"`) {
		t.Errorf("PATCH body lacks new IP: %s", w[0])
	}
}

func TestRejectedWriteFailsAndIsNotCached(t *testing.T) {
	h := newHarness(t)
	h.cf.write = `{"success":false,"errors":[{"message":"boom"}]}`
	out, ok := h.run(onlyIPv4)

	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "Cloudflare rejected the A change")
	contains(t, out, "boom")
	if _, err := os.Stat(h.cachePath("home.example.com.A")); err == nil {
		t.Errorf("failed sync must not be cached")
	}
}

func TestBothFamiliesCreated(t *testing.T) {
	h := newHarness(t)
	out, ok := h.run()

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "Creating A record: "+testIPv4)
	contains(t, out, "Creating AAAA record: "+testIPv6)
	if n := h.cf.writeCount(); n != 2 {
		t.Errorf("want 2 writes, got %d", n)
	}
}

func TestNoAddressDetected(t *testing.T) {
	h := newHarness(t)
	h.ip.ipv4, h.ip.ipv6 = "", ""
	out, ok := h.run()

	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "Failed to detect any public IP")
	contains(t, out, "   no public address detected: HTTP 503")
}

// A family that was synced before but now has no address warns that its
// record may be stale, and the cycle still succeeds on the other family.
func TestLostFamilyWarnsStale(t *testing.T) {
	h := newHarness(t)
	if out, ok := h.run(); !ok {
		t.Fatalf("first cycle failed:\n%s", out)
	}
	h.ip.ipv6 = ""
	out, ok := h.run()
	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "AAAA home.example.com may be stale: it was last set to "+testIPv6)
	contains(t, out, "CF_IPV6=false")
}

func TestIPv6OnlySkipsA(t *testing.T) {
	h := newHarness(t)
	h.ip.ipv4 = ""
	out, ok := h.run()

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "No IPv4 address detected, skipping A")
	contains(t, out, "Creating AAAA record")
}

func TestNoCreateLeavesMissingRecord(t *testing.T) {
	h := newHarness(t)
	out, ok := h.run(func(c *config.Config) { c.IPv6 = false; c.CreateMissing = false })

	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "A record for home.example.com not found")
	noWrites(t, h)
}

func TestMissingZoneFails(t *testing.T) {
	h := newHarness(t)
	h.cf.zone = `{"success":true,"result":[]}`
	out, ok := h.run(onlyIPv4)

	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "Zone 'example.com' not found")
}

func TestForceIgnoresWarmCache(t *testing.T) {
	h := newHarness(t)
	h.run(onlyIPv4)

	h.cf.record = `{"success":true,"result":[{"id":"rec1","content":"` + testIPv4 + `","ttl":1}]}`
	out, ok := h.run(func(c *config.Config) { c.IPv6 = false; c.Force = true })

	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "A already "+testIPv4+" on Cloudflare")
}

// More than one record with the name and type fails the family without any
// write, and nothing is cached, so the next cycle checks again.
func TestMultipleRecordsAreRefused(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":true,"result":[` +
		`{"id":"rec1","content":"198.51.100.1","ttl":1},` +
		`{"id":"rec2","content":"` + testIPv4 + `","ttl":1}]}`
	out, ok := h.run(onlyIPv4)

	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "Found 2 A records for home.example.com")
	noWrites(t, h)
	if _, err := os.Stat(h.cachePath("home.example.com.A")); err == nil {
		t.Errorf("failed sync must not be cached:\n%s", out)
	}
}

// Regression: changing CF_PROXIED with an unchanged IP was never applied,
// because both the cache and the record check compared only the IP.
func TestSettingsChangeIsApplied(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":true,"result":[{"id":"rec1","content":"` + testIPv4 + `","ttl":1,"proxied":false}]}`
	h.run(onlyIPv4)
	noWrites(t, h)

	out, ok := h.run(func(c *config.Config) { c.Proxied = true })
	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "Updating A settings")
	w := h.cf.lastWrites()
	if len(w) != 1 || !strings.Contains(w[0], `"proxied":true`) {
		t.Errorf("want one PATCH with proxied=true, got %v", w)
	}
}

// IPv6 addresses are compared parsed, not as text.
func TestIPv6TextVariantMatches(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":true,"result":[{"id":"rec1","content":"2606:4700:0::1","ttl":1}]}`
	out, ok := h.run(func(c *config.Config) { c.IPv4 = false })
	if !ok {
		t.Fatalf("cycle failed:\n%s", out)
	}
	contains(t, out, "AAAA already "+testIPv6)
	noWrites(t, h)
}

func TestZoneLookupErrorIsNotReportedAsMissing(t *testing.T) {
	h := newHarness(t)
	h.cf.zone = `{"success":false,"errors":[{"message":"Invalid API token"}]}`
	out, ok := h.run(onlyIPv4)
	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	contains(t, out, "Failed to look up zone 'example.com'")
	contains(t, out, "Invalid API token")
}

// An API error with no messages still prints a detail line.
func TestEmptyAPIErrorPrintsDetails(t *testing.T) {
	h := newHarness(t)
	h.cf.zone = `{"success":false,"errors":[]}`
	out, _ := h.run(onlyIPv4)
	contains(t, out, "   cloudflare API request failed (HTTP 200)")
}

// A failed record lookup must not discard a zone ID from CF_ZONE_ID, so the
// other family does not fall back to a zone lookup the token may not allow.
func TestConfiguredZoneIDSurvivesLookupFailure(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":false,"errors":[{"message":"boom"}]}`
	out, ok := h.run(func(c *config.Config) { c.ZoneID = "z1" })
	if ok {
		t.Fatal("cycle succeeded, want failure")
	}
	if h.cf.zoneLookups != 0 {
		t.Errorf("want no zone lookups with CF_ZONE_ID set, got %d:\n%s", h.cf.zoneLookups, out)
	}
}

// Without CF_ZONE_ID, a failed record lookup drops the cached zone ID.
func TestCachedZoneIDDroppedOnLookupFailure(t *testing.T) {
	h := newHarness(t)
	h.cf.record = `{"success":false,"errors":[{"message":"boom"}]}`
	h.run(onlyIPv4)
	if _, err := os.Stat(h.cachePath("zone_example.com")); err == nil {
		t.Error("cached zone ID should be removed after a failed record lookup")
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"home.example.com": "home.example.com",
		"a b/c":            "a_20b_2fc",
		"héllo":            "h_c3_a9llo",
		"*.example.com":    "_2a.example.com",
		"_.example.com":    "_5f.example.com",
		"A":                "_41",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sanity check that errors.Is still works on the sentinel values callers use.
func TestSentinelErrors(t *testing.T) {
	h := newHarness(t)
	h.ip.ipv4, h.ip.ipv6 = "", ""
	err := RunCycle(context.Background(), h.cfg, io.Discard)
	if !errors.Is(err, ErrNoAddress) {
		t.Errorf("err = %v, want ErrNoAddress", err)
	}
}
