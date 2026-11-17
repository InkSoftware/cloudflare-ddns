package ipdetect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		family Family
		ip     string
		ok     bool
	}{
		{IPv4, "1.2.3.4", true},
		{IPv4, "2606:4700::1", false},
		{IPv4, "", false},
		{IPv4, "999.1.1.1", false},
		{IPv6, "2606:4700::1", true},
		{IPv6, "1.2.3.4", false},
		{IPv6, "::ffff:1.2.3.4", false},
		{IPv6, "garbage", false},
		{IPv4, "10.0.0.1", false},
		{IPv4, "192.168.1.1", false},
		{IPv4, "127.0.0.1", false},
		{IPv4, "169.254.1.1", false},
		{IPv4, "0.0.0.0", false},
		{IPv6, "::1", false},
		{IPv6, "fe80::1", false},
		{IPv6, "fd00::1", false},
		{IPv4, "100.64.0.1", false},
		{IPv4, "192.0.2.1", false},
		{IPv4, "198.18.0.1", false},
		{IPv4, "198.51.100.1", false},
		{IPv4, "203.0.113.7", false},
		{IPv4, "240.0.0.1", false},
		{IPv4, "255.255.255.255", false},
		{IPv6, "2001:db8::1", false},
		{IPv6, "2001::1", false},
		{IPv6, "3fff::1", false},
		{IPv6, "64:ff9b::1.2.3.4", false},
	}
	for _, c := range cases {
		if got := Valid(c.family, c.ip); got != c.ok {
			t.Errorf("Valid(%s, %q) = %v, want %v", c.family, c.ip, got, c.ok)
		}
	}
}

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestDetect(t *testing.T) {
	good := serve(t, "  1.2.3.4\n", http.StatusOK)
	ip, err := Detect(context.Background(), IPv4, good.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("ip = %q, want 1.2.3.4", ip)
	}
}

func TestDetectFailures(t *testing.T) {
	for name, s := range map[string]*httptest.Server{
		"not an ip":    serve(t, "not an ip", http.StatusOK),
		"bad status":   serve(t, "1.2.3.4", http.StatusInternalServerError),
		"wrong family": serve(t, "2606:4700::1", http.StatusOK),
		"oversized":    serve(t, "1.2.3.4"+strings.Repeat(" ", 300), http.StatusOK),
	} {
		if _, err := Detect(context.Background(), IPv4, s.URL); !errors.Is(err, ErrNoAddress) {
			t.Errorf("%s: err = %v, want ErrNoAddress", name, err)
		}
	}
}

func TestDetectRefusesRedirect(t *testing.T) {
	target := serve(t, "1.2.3.4", http.StatusOK)
	s := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	t.Cleanup(s.Close)
	_, err := Detect(context.Background(), IPv4, s.URL)
	if !errors.Is(err, ErrNoAddress) || !strings.Contains(err.Error(), "HTTP 302") {
		t.Errorf("err = %v, want ErrNoAddress naming HTTP 302", err)
	}
}

func TestDetectReturnsCanonicalForm(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "2606:4700:0:0::1")
	}))
	s.Listener.Close()
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)

	ip, err := Detect(context.Background(), IPv6, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "2606:4700::1" {
		t.Errorf("ip = %q, want 2606:4700::1", ip)
	}
}
