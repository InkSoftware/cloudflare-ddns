# CLAUDE.md

## Commands

CI runs these; all must pass:

```sh
gofmt -l .                  # must print nothing
go vet ./...
go test -race -count=1 ./...
```

Single test: `go test -race -run TestName ./internal/updater`
Local run: `CF_TOKEN=... CF_ZONE=example.com CF_RECORD=home.example.com go run ./cmd/cloudflare-ddns once` (`FORCE=1` bypasses the cache).

## Constraints

- **Standard library only**, with no dependencies in `go.mod`. The image is `scratch` (CA certs only, no tzdata). It runs read-only as UID 10001 with a 16 MB memory limit.
- Config comes only from env vars, parsed in `internal/config` (`load` takes a `getenv` func for tests). A new setting needs updates to `Config`, `load`, the README table and `docker-compose.yml`. Compose passes optional vars as `${VAR:-}`, and an empty value means the default. Exception: `CACHE_DIR` and `HEALTH_FILE` stay out of compose, since they must sit on the `/tmp` tmpfs and the healthcheck uses the default path.
- The API base URL and IP-echo URLs are `Config` fields, not env vars, so tests can point them at `httptest` servers.

## Architecture

`cmd/cloudflare-ddns` (dispatch, exit codes 0/1/2, signal context) → `internal/app` (`Daemon`, `Once`, `Healthcheck`) → `internal/updater.RunCycle` → `internal/ipdetect` + `internal/cloudflare`.

- **Each cycle builds a fresh `updater`.** A and AAAA are handled independently. A family with no address is skipped (`ErrNoAddress`), with a stale-record warning if it has a cache entry; its record is never deleted. The cycle fails only if no family had one. Any sync failure → `ErrSyncFailed`.
- **Cache** (`CACHE_DIR`): `<record>.A`/`.AAAA` hold `"<ip> ttl=N proxied=B"` from the last successful sync. `zone_<zone>` holds the zone ID. If the entry matches and is younger than `CACHE_MAX_AGE`, Cloudflare is not contacted. A failed record lookup deletes the cached zone ID (an ID from `CF_ZONE_ID` is always kept). Zone ID priority: `CF_ZONE_ID` → cache → API.
- **Record sync**: exactly one record per name and type (A or AAAA) is managed. If more than one exists, the family fails with no writes. The record is compared on parsed IP, `proxied` and TTL; TTL only counts when unproxied, since Cloudflare forces auto TTL on proxied records. It is updated only if it differs, with PATCH so comments and tags survive. A record is created only when none exist.
- **Health file** holds a Unix *expiry* (now + interval + 300s). `Daemon` deletes it on start and rewrites it only after a successful cycle. After a failed cycle it retries with backoff (30s doubling, capped at the interval). `healthcheck` reads only `HEALTH_FILE`.
- **`internal/fsutil` is security-sensitive.** The cache dir must be a real 0700 dir owned by the current user. Reads open with `O_NOFOLLOW|O_NONBLOCK`, then check the opened file: it must be regular, owned by the current user and at most 4 KiB. Writes are atomic (temp file + rename). Use it for any on-disk state.
- **IP detection** (`internal/ipdetect`) asks one service per family (`ipv4.icanhazip.com` / `ipv6.icanhazip.com`, run by Cloudflare) with no fallback. It never uses a proxy, because the echo service would see the proxy's IP, and never follows redirects. It accepts only public addresses (no private, loopback, link-local, CGNAT, documentation or other special-purpose blocks; IPv6 must be in 2000::/3) and returns them in canonical form.
- **Output**: emoji-prefixed lines (`✅ 🔄 ➕ ❌ ⚠️ 🌍`) written to an `io.Writer`. `printDetails` indents `APIError` messages under the failure line, or the error text (with HTTP status) when there are none.

## Tests

Stdlib only. `internal/updater/updater_test.go` has `fakeCF` (fake API that records POST/PATCH writes) and `ipServer` (tells IPv4 from IPv6 by the client's remote address). Reuse them.

## Release

Pushes to `main`, `v*` tags and a nightly schedule build amd64/arm64 images to `ghcr.io/inksoftware/cloudflare-ddns`. A `vX.Y.Z` tag publishes `X`, `X.Y` and `X.Y.Z`. Nightly builds use `no-cache` and add `nightly` plus a date tag; the `refresh-release` job also rebuilds the newest `vX.Y.Z` and re-pushes its `X`, `X.Y`, `X.Y.Z` tags. `docker-compose.synology.yml` removes the resource limits.
