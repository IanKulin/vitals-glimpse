# Security Feature Tests

Tests for the vitals-glimpse security features (API key, IP allowlist, rate limiting). Runs on macOS using Docker.

## Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) installed and running

## Quick Start

From the **repo root**:

```bash
./test/test-security.sh
```

The script builds the image, runs the tests, and cleans up after itself.

## What Gets Tested

| # | Feature | What it checks |
|---|---------|----------------|
| 1 | API Key (`-key`) | 401 with no key, 401 with wrong key, 200 with correct key |
| 2 | Rate Limiting (`-ratelimit`) | 200 for requests within limit, 429 after exceeding it |
| 3 | IP Allowlist (`-allow`) | 403 from a non-allowed IP, 200 from an allowed IP |
| 4 | Open Access | 200 with no security flags, valid JSON response |

## How It Works

1. Builds a multi-stage Docker image from `test/Dockerfile`
2. Creates a Docker network (`vg-testnet`) for IP allowlist testing
3. For each test, starts a fresh container with the relevant flags
4. Uses `curl` from the host (and from a second container for allowlist tests)
5. Cleans up all containers and the network on exit

## Dockerfile

The `Dockerfile` uses a two-stage build:

- **Build stage**: `golang:1.21-alpine` compiles the binary
- **Runtime stage**: `alpine:3.19` runs it (~5 MB image)

You can also use the Dockerfile independently:

```bash
docker build -t vitals-glimpse -f test/Dockerfile .
docker run -p 10321:10321 vitals-glimpse -key mysecret -ratelimit 30
```

## Troubleshooting

**Port 10321 already in use** — stop whatever is using it, or edit `PORT=10321` in the script.

**Docker not running** — start Docker Desktop first.

**Allowlist test fails** — the test assumes Docker's default bridge network uses `172.16.0.0/12`. If your Docker is configured with a different subnet, adjust the `-allow` CIDR in the script.
