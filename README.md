# vitals-glimpse

`vitals-glimpse` is a simple endpoint that exposes a Linux host's (or container's) memory, disk, and CPU usage as percentages.

The JSON returned also has status keywords for the memory, disk, and CPU which makes it a good candidate for simple monitoring tools such as [Uptime Kuma](https://github.com/louislam/uptime-kuma) which can monitor a binary up/down response based on the presence or absence of a keyword. The thresholds for these have defaults that can be overwritten with command line flags.

To access the endpoint in a browser, visit `<server name>:10321/vitals`

## Output

A sample response might be:
```
{
	"title": "vitals-glimpse",
	"version": 0.6,
	"mem_status": "mem_okay",
	"mem_percent": 37,
	"disk_status": "disk_okay",
	"disk_percent": 15,
	"cpu_status": "cpu_okay",
	"cpu_percent": 2,
	"disk_iops_status": "disk_iops_okay",
	"disk_iops": 312,
	"net_in_kbs": 123,
	"net_out_kbs": 45,
	"ts_expiry_status": "ts_expiry_okay",
	"ts_expiry_days": 62
}
```
or if the thresholds are exceeded:
```
{
	"title": "vitals-glimpse",
	"version": 0.6,
	"mem_status": "mem_fail",
	"mem_percent": 91,
	"disk_status": "disk_fail",
	"disk_percent": 81,
	"cpu_status": "cpu_fail",
	"cpu_percent": 92,
	"disk_iops_status": "disk_iops_fail",
	"disk_iops": 850,
	"net_in_kbs": 123,
	"net_out_kbs": 45,
	"ts_expiry_status": "ts_expiry_fail",
	"ts_expiry_days": 3
}
```
The disk usage is based on the `/` root mount point.

The default thresholds for the status keywords are:
* `mem_okay` - below 90% memory usage
* `disk_okay` - below 80% disk usage
* `cpu_okay` - below 90% CPU usage
* `disk_iops_okay` - below 400 IOPS (reads + writes per second); omitted when running inside a container (LXC/Docker) since `/proc/diskstats` reflects host-level activity rather than per-container I/O
* `net_in_kbs` / `net_out_kbs` - network throughput in KB/s (1024 bytes) on the default-route interface; no status keyword or threshold, omitted if no interface is found
* `ts_expiry_okay` - Tailscale key expiry more than 7 days away (`-ts_expiry`); `ts_expiry_fail` otherwise. `ts_expiry_days` gives the days remaining. The whole section is omitted if Tailscale is not present, and `ts_expiry_days` is omitted when key expiry is disabled.

`cpu_percent`, `disk_iops` and the network figures are averages over the last 30 seconds (see `-window`), sampled in the background every 5 seconds, so a short spike will not immediately trip `cpu_fail` or `disk_iops_fail`. Requests return immediately.

## Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-mem` | 90 | Memory usage threshold (percent) |
| `-disk` | 80 | Disk usage threshold (percent) |
| `-cpu` | 90 | CPU usage threshold (percent) |
| `-disk_iops` | 400 | IOPS threshold (reads + writes per second) |
| `-ts_expiry` | 7 | Days-until-Tailscale-key-expiry threshold for pass/fail |
| `-iodev` | _(auto)_ | Device name to measure (e.g. `sda`). Auto-detected if omitted. |
| `-iface` | _(auto)_ | Network interface to measure (e.g. `eth0`). Auto-detected from the default route if omitted. |
| `-window` | 30 | Averaging window in seconds for CPU, IOPS and network rates (multiple of 5, 5-60) |
| `-port` | 10321 | Server port |
| `-bind` | `0.0.0.0` | Address to bind to |
| `-key` | _(none)_ | API key required via `X-API-Key` request header |
| `-allow` | _(none)_ | Comma-separated CIDR allowlist; requests from other IPs are rejected with 403 |
| `-ratelimit` | 60 | Max requests per IP per minute (0 to disable); allowlisted IPs are exempt |

Example:
```
./vitals-glimpse -mem 85 -disk 70 -cpu 95 -port 8080
```

## Security

v0.4 adds several hardening options for deployments exposed beyond a trusted network.

### API Key Authentication

Require callers to supply a secret key in the `X-API-Key` header:

```
./vitals-glimpse -key mysecretkey
```

```
curl -H "X-API-Key: mysecretkey" http://<server>:10321/vitals
```

Requests without the correct key receive `401 Unauthorized`. Key comparison uses constant-time comparison to prevent timing attacks.

### IP Allowlisting

Restrict access to specific IP addresses or CIDR ranges. Plain IPs are accepted as well as CIDR notation:

```
./vitals-glimpse -allow "10.0.0.0/24,192.168.1.50"
```

Requests from addresses outside the allowlist receive `403 Forbidden`.

### Rate Limiting

By default, each client IP is limited to 60 requests per minute. Allowlisted IPs (via `-allow`) are exempt from the rate limit. Set to 0 to disable entirely:

```
./vitals-glimpse -ratelimit 120   # 120 req/min
./vitals-glimpse -ratelimit 0     # no limit
```

Requests exceeding the limit receive `429 Too Many Requests`.

### Combining Options

```
./vitals-glimpse -key mysecretkey -allow "10.0.0.0/8" -ratelimit 30 -bind 127.0.0.1
```

## Development

### Building on MacBook
- `GOARCH=amd64 GOOS=linux go build`
- `GOARCH=amd64 GOOS=linux go build -ldflags="-s -w"` for distribution
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w"` for Synology
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w"` for 32bit Synology

The `-s -w` flags strip debug info and symbol tables, reducing binary size by ~20-30%.

### Testing on Debian LXC
Remove an installed version
- `sudo systemctl stop vitals-glimpse`
- `sudo systemctl disable vitals-glimpse`
Copy up the new one
- `scp vitals-glimpse ian@ct390-test:vitals-glimpse`
- `ssh ian@ct390-test 'chmod +x vitals-glimpse'`
Run it in the background if you're not going to just ssh n and run it manually
- `ssh ian@ct390-test 'nohup ./vitals-glimpse > output.log 2>&1 & echo $! > vitals-glimpse.pid'`
- `http://ct390-test:10321/vitals`
- `ssh ian@ct390-test 'kill $(cat vitals-glimpse.pid)'`

## AI Disclosure

AI tools were used in later versions of this software

## Versions

- 0.2 MVP
- 0.3 Container detection for CPU, flags for thresholds
- 0.4 Security (API key auth, IP allowlisting, rate limiting, HTTP timeouts)
- 0.5 Disk IOPS metric (reads + writes/sec from `/proc/diskstats`
- 0.6 Tailscale key expiry (`ts_expiry_status`, `ts_expiry_days`)
- 0.7 IOPS omitted in containers (LXC/Docker) — `/proc/diskstats` is not namespaced and reflects host activity
- 0.8 Background 5s sampling with windowed averages for CPU/IOPS; network throughput (`net_in_kbs`, `net_out_kbs`); `-iface` and `-window` flags
