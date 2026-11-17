// Package ipdetect finds this host's public IPv4 or IPv6 address by asking an
// external echo service.
package ipdetect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrNoAddress is returned by Detect when the service gave no valid address.
var ErrNoAddress = errors.New("no public address detected")

// Family selects the IP version. Its value is the version number.
type Family int

const (
	IPv4 Family = 4
	IPv6 Family = 6
)

func (f Family) String() string {
	return fmt.Sprintf("IPv%d", int(f))
}

// network is the transport used to dial the echo services. Forcing tcp4 or
// tcp6 makes each service see this host's address of that family.
func (f Family) network() string {
	if f == IPv6 {
		return "tcp6"
	}
	return "tcp4"
}

// Detect asks the echo service at url for this host's address of family f
// and returns it in canonical form. If the request fails, the status is not
// 200, or the answer is not a public address of that family, it returns an
// error that wraps ErrNoAddress and says why.
func Detect(ctx context.Context, f Family, url string) (string, error) {
	// Clone the default transport for its timeouts, but never use a proxy:
	// the echo service would then report the proxy's address, not ours.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, f.network(), addr)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		// Never follow a redirect: the answer must come from the configured
		// service, not from wherever it points. A 3xx fails as a bad status.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoAddress, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoAddress, err)
	}
	defer resp.Body.Close()

	// An address is far shorter than 256 bytes. A longer body is junk, even
	// if it starts with something that looks like an address.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d from %s", ErrNoAddress, resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 257))
	if err != nil {
		return "", fmt.Errorf("%w: read response: %w", ErrNoAddress, err)
	}
	if len(body) > 256 {
		return "", fmt.Errorf("%w: response from %s is too long", ErrNoAddress, url)
	}

	// Strip all whitespace, not just the trailing newline, in case the
	// service pads its output.
	ip := strings.Join(strings.Fields(string(body)), "")
	if !Valid(f, ip) {
		return "", fmt.Errorf("%w: %q is not a public %s address", ErrNoAddress, ip, f)
	}
	return netip.MustParseAddr(ip).String(), nil
}

// Valid reports whether s is a public textual address of family f. Private,
// loopback, link-local, special-purpose and other non-global addresses are
// rejected, so a captive portal or misbehaving service cannot point the record
// at them. For IPv6, an IPv4-mapped address such as ::ffff:1.2.3.4 is
// rejected, because it is an IPv4 address in IPv6 notation.
func Valid(f Family, s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(addr) {
			return false
		}
	}
	switch f {
	case IPv4:
		return addr.Is4()
	case IPv6:
		// Every global unicast IPv6 allocation is inside 2000::/3.
		return addr.Is6() && !addr.Is4In6() && globalIPv6.Contains(addr)
	}
	return false
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")

// nonPublic lists special-purpose blocks (from the IANA registries) that pass
// IsGlobalUnicast and !IsPrivate but are never a host's public address.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "This network".
	netip.MustParsePrefix("100.64.0.0/10"),   // Shared address space (CGNAT).
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments.
	netip.MustParsePrefix("192.0.2.0/24"),    // Documentation (TEST-NET-1).
	netip.MustParsePrefix("192.88.99.0/24"),  // Deprecated 6to4 relay anycast.
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmarking.
	netip.MustParsePrefix("198.51.100.0/24"), // Documentation (TEST-NET-2).
	netip.MustParsePrefix("203.0.113.0/24"),  // Documentation (TEST-NET-3).
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved, and broadcast.
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments (Teredo, ORCHID, benchmarking).
	netip.MustParsePrefix("2001:db8::/32"),   // Documentation.
	netip.MustParsePrefix("3fff::/20"),       // Documentation.
}
