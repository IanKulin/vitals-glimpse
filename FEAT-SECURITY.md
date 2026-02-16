# FEAT-SECURITY: Security Hardening

Implementation plan for adding security features to vitals-glimpse.

## New CLI Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-bind` | string | `"0.0.0.0"` | Address to bind the listener to |
| `-key` | string | `""` (disabled) | API key required via `X-API-Key` header |
| `-allow` | string | `""` (disabled) | Comma-separated CIDR allowlist (e.g. `"10.0.0.0/24,192.168.1.0/24"`) |
| `-ratelimit` | int | `60` | Max requests per IP per minute (fixed window, 0 to disable) |

## Implementation Order

Each feature is independent. Implement them in this order so each layer wraps the next cleanly as middleware.

### 1. Bind Address + Server Timeouts

**What changes:** Replace `http.ListenAndServe` with a configured `http.Server`.

```go
var bindAddr = "0.0.0.0"

// in main():
flag.StringVar(&bindAddr, "bind", "0.0.0.0", "address to bind to")

// in handleRequests():
server := &http.Server{
    Addr:         fmt.Sprintf("%s:%d", bindAddr, port),
    ReadTimeout:  5 * time.Second,
    WriteTimeout: 10 * time.Second,  // CPU measurement takes ~1s
    IdleTimeout:  30 * time.Second,
}
log.Fatal(server.ListenAndServe())
```

No new imports needed — `net/http` and `time` are already imported.

### 2. API Key Check

**What changes:** New global `var apiKey string`, new flag, new middleware function.

When `-key` is not set, skip the check entirely (open access, current behavior). When set, every request must include `X-API-Key: <value>` or receive `401 Unauthorized`.

```go
var apiKey string

func requireKey(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if apiKey != "" && r.Header.Get("X-API-Key") != apiKey {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }
        next(w, r)
    }
}
```

Wire up in `handleRequests()`:
```go
http.HandleFunc(endPoint, requireKey(serveStats))
http.HandleFunc("/", requireKey(serveStats))
```

Use constant-time comparison (`crypto/subtle.ConstantTimeCompare`) to prevent timing attacks. New import: `crypto/subtle`.

### 3. IP Allowlist (CIDR)

**What changes:** New global `var allowCIDRs string`, parsed at startup into `[]*net.IPNet`. New middleware function.

When `-allow` is not set, all IPs are permitted (current behavior). When set, only requests from matching CIDRs are served; all others get `403 Forbidden`.

```go
var allowCIDRs string
var allowedNets []*net.IPNet

// in main(), after flag.Parse():
if allowCIDRs != "" {
    for _, cidr := range strings.Split(allowCIDRs, ",") {
        _, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
        if err != nil {
            log.Fatalf("invalid -allow CIDR %q: %v", cidr, err)
        }
        allowedNets = append(allowedNets, network)
    }
}
```

Middleware:
```go
func requireAllowedIP(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if len(allowedNets) > 0 {
            host, _, _ := net.SplitHostPort(r.RemoteAddr)
            ip := net.ParseIP(host)
            allowed := false
            for _, n := range allowedNets {
                if n.Contains(ip) {
                    allowed = true
                    break
                }
            }
            if !allowed {
                http.Error(w, "Forbidden", http.StatusForbidden)
                return
            }
        }
        next(w, r)
    }
}
```

New import: `net`.

Also accept bare IPs in the `-allow` flag (e.g. `192.168.1.5`) by auto-appending `/32` or `/128` during parsing.

### 4. Rate Limiting (Fixed Window)

**What changes:** New global `var rateLimit int`, in-memory map tracking request counts per IP per minute window. New middleware function.

Defaults to 60 requests per minute per IP. Set to `0` to disable. Exceeding the limit returns `429 Too Many Requests`.

```go
var rateLimit int

type rateLimiter struct {
    mu       sync.Mutex
    counts   map[string]int  // key: "ip|minute_timestamp"
    lastClean int64
}

var limiter = &rateLimiter{counts: make(map[string]int)}

func (rl *rateLimiter) allow(ip string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    now := time.Now().Unix() / 60  // current minute
    key := ip + "|" + strconv.FormatInt(now, 10)

    // Clean stale entries every new minute
    if now != rl.lastClean {
        for k := range rl.counts {
            if !strings.HasSuffix(k, "|"+strconv.FormatInt(now, 10)) {
                delete(rl.counts, k)
            }
        }
        rl.lastClean = now
    }

    rl.counts[key]++
    return rl.counts[key] <= rateLimit
}
```

Middleware:
```go
func rateCheck(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if rateLimit > 0 {
            host, _, _ := net.SplitHostPort(r.RemoteAddr)
            if !limiter.allow(host) {
                http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
                return
            }
        }
        next(w, r)
    }
}
```

New import: `sync`.

Allowlisted IPs (from `-allow`) bypass rate limiting — they've already been explicitly trusted.

### 5. Wire Up Middleware Chain

Order matters. Outermost check runs first:

```
IP Allowlist → Rate Limit → API Key → serveStats
```

In `handleRequests()`:
```go
handler := requireAllowedIP(rateCheck(requireKey(serveStats)))
http.HandleFunc(endPoint, handler)
http.HandleFunc("/", handler)
```

This means:
1. Blocked IPs are rejected immediately (no resources wasted)
2. Allowed IPs that exceed rate limits get 429
3. Rate-limited IPs that pass still need a valid API key
4. Only then is the (expensive, 1s CPU sample) handler called

## New Imports Summary

```go
"crypto/subtle"  // constant-time key comparison
"net"            // CIDR parsing, IP matching
"sync"           // mutex for rate limiter
```

## Startup Log Output

Print active security config at startup so the operator knows what's enabled:

```
vitals-glimpse listening on 10.0.0.5:10321
  API key: required
  Allowed CIDRs: 10.0.0.0/24, 192.168.1.0/24
  Rate limit: 30 req/min per IP
```

Omit lines for features that are not enabled.
