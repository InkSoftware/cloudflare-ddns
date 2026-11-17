# cloudflare-ddns

Keeps Cloudflare A/AAAA records pointed at this host's public IP (as reported by `icanhazip.com`). Exactly one record per type is managed; if the name has more than one A (or AAAA) record, that type is not touched and the cycle fails.

```sh
cp .env.example .env   # set CF_TOKEN, CF_ZONE, CF_RECORD
docker compose up -d
```

Without Docker: `cloudflare-ddns [once|healthcheck]` (no argument runs the daemon).

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `CF_TOKEN` | required | API token with Zone:DNS:Edit |
| `CF_ZONE` | required | Zone, e.g. `example.com` |
| `CF_RECORD` | required | Full record name inside `CF_ZONE`, e.g. `home.example.com` (case and a trailing dot are ignored) |
| `CF_ZONE_ID` | | Skips the zone lookup |
| `CF_TTL` | `1` | `1` = automatic, else 60–86400 |
| `CF_PROXIED` | `false` | Proxy through Cloudflare |
| `CF_IPV4` / `CF_IPV6` | `true` | Manage A / AAAA |
| `CF_CREATE_MISSING` | `true` | Create the record if absent |
| `DDNS_INTERVAL` | `3600` | Seconds between runs (max 604800). After a failure, retries start at 30s and double up to this value |
| `CACHE_DIR` | `/tmp/cf-ddns` | Cache directory |
| `CACHE_MAX_AGE` | `1440` | Minutes before re-checking Cloudflare (`0` = never, max 525600) |
| `FORCE` | `false` | Ignore the cache |
| `HEALTH_FILE` | `/tmp/healthy` | Health file path |

If one family stops working (for example the host loses IPv6), its record is
left as it is and a warning says it may be stale. Disable that family
(`CF_IPV6=false`) or delete the record by hand.

## License

MIT
