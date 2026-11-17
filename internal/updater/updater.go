// Package updater runs one DDNS update cycle: it detects the public IP, checks
// the local cache, and makes the Cloudflare records match.
package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/InkSoftware/cloudflare-ddns/internal/cloudflare"
	"github.com/InkSoftware/cloudflare-ddns/internal/config"
	"github.com/InkSoftware/cloudflare-ddns/internal/fsutil"
	"github.com/InkSoftware/cloudflare-ddns/internal/ipdetect"
)

var (
	// ErrNoAddress means no enabled IP family produced an address.
	ErrNoAddress = errors.New("failed to detect any public IP")
	// ErrSyncFailed means at least one enabled family could not be updated.
	ErrSyncFailed = errors.New("one or more records failed to update")

	errRecordMissing   = errors.New("record not found and CF_CREATE_MISSING is false")
	errMultipleRecords = errors.New("more than one record with the configured name and type")
)

// RunCycle performs one update pass for each IP family enabled in cfg.
// Progress is written to out. It returns nil when every enabled family that
// had an address is in sync. A family with no detectable address does not
// count as a failure as long as another family succeeded.
func RunCycle(ctx context.Context, cfg config.Config, out io.Writer) error {
	if err := fsutil.PrepareCacheDir(cfg.CacheDir); err != nil {
		fmt.Fprintf(out, "❌ Cannot use cache dir %s: %v\n", cfg.CacheDir, err)
		return err
	}

	u := &updater{
		cfg:       cfg,
		api:       cloudflare.New(cfg.APIBase, cfg.Token),
		out:       out,
		zoneID:    cfg.ZoneID,
		zoneCache: filepath.Join(cfg.CacheDir, "zone_"+sanitize(cfg.Zone)),
		recordKey: sanitize(cfg.Record),
	}
	return u.run(ctx)
}

// updater holds the state for one cycle. A new updater is built for each
// cycle, so a zone ID that went stale is looked up again on the next cycle.
type updater struct {
	cfg config.Config
	api *cloudflare.Client
	out io.Writer

	zoneID    string // Resolved zone ID; empty until ensureZone succeeds.
	zoneCache string // Path of the cached zone ID file.
	recordKey string // Sanitized record name, used in per-record cache file names.
}

func (u *updater) run(ctx context.Context) error {
	var families []ipdetect.Family
	if u.cfg.IPv4 {
		families = append(families, ipdetect.IPv4)
	}
	if u.cfg.IPv6 {
		families = append(families, ipdetect.IPv6)
	}

	found, failed := false, false
	for _, f := range families {
		switch err := u.process(ctx, f); {
		case errors.Is(err, ErrNoAddress):
			// Already reported; another family may still succeed.
		case err != nil:
			found, failed = true, true
		default:
			found = true
		}
	}

	if !found {
		fmt.Fprintln(u.out, "❌ Failed to detect any public IP")
		return ErrNoAddress
	}
	if failed {
		return ErrSyncFailed
	}
	return nil
}

// process detects the public IP for one family and syncs its record.
//
// The last synced IP, TTL and proxy setting are cached per record and type. If
// they are unchanged and the cache entry is younger than CacheMaxAge,
// Cloudflare is not called. The periodic refresh means a record changed by
// hand on Cloudflare is eventually corrected. FORCE=1 skips the cache entirely.
func (u *updater) process(ctx context.Context, f ipdetect.Family) error {
	typ := recordType(f)

	cache := filepath.Join(u.cfg.CacheDir, u.recordKey+"."+typ)
	cached := fsutil.ReadTrimmed(cache)
	last, _, _ := strings.Cut(cached, " ")

	ip, err := ipdetect.Detect(ctx, f, ipURL(u.cfg, f))
	if err != nil {
		fmt.Fprintf(u.out, "⚠️  No %s address detected, skipping %s\n", f, typ)
		printDetails(u.out, err)
		if last != "" {
			// The record is left alone, so it may now point at an address
			// this host no longer has.
			fmt.Fprintf(u.out, "⚠️  %s %s may be stale: it was last set to %s. If this host lost %s, set CF_%s=false or delete the record.\n",
				typ, u.cfg.Record, last, f, strings.ToUpper(f.String()))
		}
		return ErrNoAddress
	}

	entry := fmt.Sprintf("%s ttl=%d proxied=%t", ip, u.cfg.TTL, u.cfg.Proxied)
	stale := u.cfg.CacheMaxAge > 0 && fsutil.IsOlderThan(cache, u.cfg.CacheMaxAge)

	if !u.cfg.Force && cached == entry && !stale {
		fmt.Fprintf(u.out, "✅ %s %s unchanged (cached)\n", typ, ip)
		return nil
	}

	if last != "" {
		fmt.Fprintf(u.out, "🌍 %s: %s (cached: %s)\n", f, ip, last)
	} else {
		fmt.Fprintf(u.out, "🌍 %s: %s\n", f, ip)
	}

	if err := u.syncRecord(ctx, typ, ip); err != nil {
		// Leave the cache alone so the next cycle retries, instead of
		// treating a failed IP as already synced.
		return err
	}

	// A cache write failure only costs one extra API call next cycle.
	_ = fsutil.WriteFileAtomic(cache, []byte(entry+"\n"))
	return nil
}

// ensureZone sets u.zoneID, looking it up if needed. The lookup order is the
// configured ZoneID, then the on-disk cache, then the API.
func (u *updater) ensureZone(ctx context.Context) error {
	if u.zoneID != "" {
		return nil
	}
	if cached := fsutil.ReadTrimmed(u.zoneCache); cached != "" {
		u.zoneID = cached
		return nil
	}

	id, err := u.api.ZoneID(ctx, u.cfg.Zone)
	if errors.Is(err, cloudflare.ErrZoneNotFound) {
		fmt.Fprintf(u.out, "❌ Zone '%s' not found\n", u.cfg.Zone)
		return err
	}
	if err != nil {
		fmt.Fprintf(u.out, "❌ Failed to look up zone '%s'\n", u.cfg.Zone)
		printDetails(u.out, err)
		return err
	}
	u.zoneID = id
	_ = fsutil.WriteFileAtomic(u.zoneCache, []byte(id+"\n"))
	return nil
}

// syncRecord makes the Cloudflare record of type typ with the configured name
// point at ip with the configured TTL and proxy setting. If none exists, it
// creates one when CF_CREATE_MISSING is set. If more than one exists, it
// refuses to touch any of them: setting them all to the same IP would fail
// with Cloudflare's "identical record" error and would overwrite records that
// may belong to other hosts.
func (u *updater) syncRecord(ctx context.Context, typ, ip string) error {
	if err := u.ensureZone(ctx); err != nil {
		return err
	}

	recs, err := u.api.FindRecords(ctx, u.zoneID, typ, u.cfg.Record)
	if err != nil {
		fmt.Fprintf(u.out, "❌ Failed to fetch %s records\n", typ)
		printDetails(u.out, err)

		// A cached or looked-up zone ID may be stale, for example after the
		// zone was deleted and recreated. Drop it so it is looked up again.
		// An ID from CF_ZONE_ID is the user's choice and is always kept.
		if u.cfg.ZoneID == "" {
			_ = os.Remove(u.zoneCache)
			u.zoneID = ""
		}
		return err
	}

	spec := cloudflare.RecordSpec{
		Type:    typ,
		Name:    u.cfg.Record,
		Content: ip,
		TTL:     u.cfg.TTL,
		Proxied: u.cfg.Proxied,
	}

	switch len(recs) {
	case 0:
		if !u.cfg.CreateMissing {
			fmt.Fprintf(u.out, "❌ %s record for %s not found\n", typ, u.cfg.Record)
			return errRecordMissing
		}
		fmt.Fprintf(u.out, "➕ Creating %s record: %s\n", typ, ip)
		return u.write(ctx, typ, "", spec)
	case 1:
	default:
		fmt.Fprintf(u.out, "❌ Found %d %s records for %s; remove the extras so only one is left\n", len(recs), typ, u.cfg.Record)
		return errMultipleRecords
	}

	rec := recs[0]
	if u.matches(rec, ip) {
		fmt.Fprintf(u.out, "✅ %s already %s on Cloudflare\n", typ, ip)
		return nil
	}
	if rec.Content != ip {
		fmt.Fprintf(u.out, "🔄 Updating %s: %s → %s\n", typ, rec.Content, ip)
	} else {
		fmt.Fprintf(u.out, "🔄 Updating %s settings: ttl=%d proxied=%t\n", typ, u.cfg.TTL, u.cfg.Proxied)
	}
	return u.write(ctx, typ, rec.ID, spec)
}

// write creates (empty recID) or updates one record and reports the outcome.
func (u *updater) write(ctx context.Context, typ, recID string, spec cloudflare.RecordSpec) error {
	if err := u.api.WriteRecord(ctx, u.zoneID, recID, spec); err != nil {
		fmt.Fprintf(u.out, "❌ Cloudflare rejected the %s change\n", typ)
		printDetails(u.out, err)
		return err
	}
	fmt.Fprintf(u.out, "✅ %s set to %s\n", typ, spec.Content)
	return nil
}

// matches reports whether rec already points at ip with the configured
// settings. Addresses are compared parsed, so textual variants of the same
// IPv6 address match. Cloudflare forces TTL to automatic (1) on proxied
// records, so TTL is only compared for unproxied ones.
func (u *updater) matches(rec cloudflare.Record, ip string) bool {
	a, err1 := netip.ParseAddr(rec.Content)
	b, err2 := netip.ParseAddr(ip)
	if err1 != nil || err2 != nil || a != b {
		return false
	}
	if rec.Proxied != u.cfg.Proxied {
		return false
	}
	return u.cfg.Proxied || rec.TTL == u.cfg.TTL
}

// printDetails writes the error's detail lines, indented under the failure
// line the caller already printed. An APIError without messages falls back to
// its Error text, which names the HTTP status.
func printDetails(out io.Writer, err error) {
	var apiErr *cloudflare.APIError
	if errors.As(err, &apiErr) && len(apiErr.Messages) > 0 {
		for _, m := range apiErr.Messages {
			fmt.Fprintln(out, "   "+m)
		}
		return
	}
	fmt.Fprintln(out, "   "+err.Error())
}

func recordType(f ipdetect.Family) string {
	if f == ipdetect.IPv6 {
		return "AAAA"
	}
	return "A"
}

func ipURL(cfg config.Config, f ipdetect.Family) string {
	if f == ipdetect.IPv6 {
		return cfg.IPv6URL
	}
	return cfg.IPv4URL
}

// sanitize maps s to a string that is safe in a file name. Bytes in
// [a-z0-9.-] are kept and every other byte becomes '_' plus two lowercase hex
// digits, so '*' becomes "_2a" and '_' becomes "_5f". The mapping is
// reversible, so two different names never share a cache file.
func sanitize(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}
