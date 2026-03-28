# MTProxy

MTProto proxy server written in Go. Allows any Telegram client to connect through your server. Supports `dd` (obfuscated2) and `ee` (fake-TLS) secret types, multiple concurrent users, and per-secret statistics.

## Features

- **dd-secrets** — obfuscated2 transport (AES-256-CTR)
- **ee-secrets** — fake-TLS transport, traffic looks like HTTPS to middleboxes
- **Multiple secrets** — different secrets for different users, all tracked separately
- **Statistics** — HTTP endpoint with live connection and traffic counters
- **Zero dependencies** — only the Go standard library

## Requirements

- Go 1.22+
- Linux (recommended), macOS or Windows
- Port 443 requires `root` or `CAP_NET_BIND_SERVICE`

## Quick Start

```bash
# 1. Clone
git clone https://github.com/yourname/mtproxy
cd mtproxy

# 2. Generate a secret
SECRET=$(openssl rand -hex 16)
echo "dd${SECRET}"   # share this with users

# 3. Build
make linux           # static Linux amd64 binary
# or
go build -o mtproxy ./cmd/mtproxy

# 4. Run
SECRETS="myuser:dd${SECRET}" PORT=443 ./mtproxy
```

At startup the server prints a ready-to-use `tg://` link for each secret:

```
[mtproxy] MTProxy listening on 0.0.0.0:443 (1 secrets)
[mtproxy]   [0] myuser  type=dd  link: tg://proxy?server=1.2.3.4&port=443&secret=dd8f3a...
```

Share the link with users — they paste it into **Telegram → Settings → Proxy**.

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRETS` | *(required)* | Comma-separated list of `[name:]secret` |
| `BIND_ADDR` | `0.0.0.0` | Listen address |
| `PORT` | `443` | Listen port |
| `TIMEOUT_SEC` | `120` | Idle connection timeout (seconds) |
| `STATS_ADDR` | `:8080` | HTTP stats server address |

### Secret formats

```
# Plain / dd-secret (obfuscated2):
dd<32 hex chars>
Example: dd0102030405060708090a0b0c0d0e0f

# ee-secret (fake-TLS, domain is optional):
ee<32 hex chars>[<domain in hex>]
Example: ee0102030405060708090a0b0c0d0e0f676f6f676c652e636f6d
         ^^                              ^^^^^^^^^^^^^^^^^^^^
         ee                              google.com in hex
```

Generate secrets:

```bash
# dd-secret
echo "dd$(openssl rand -hex 16)"

# ee-secret with domain google.com
echo "ee$(openssl rand -hex 16)$(echo -n 'google.com' | xxd -p)"
```

### Multiple secrets example

```bash
export SECRETS="alice:dd8f3a...,bob:ee9d1c...676f6f676c652e636f6d"
export PORT=443
export STATS_ADDR=":8080"
./mtproxy
```

### Using a .env file

```bash
cp .env.example .env
# edit .env
source .env && ./mtproxy
```

## Statistics

The stats HTTP server exposes two endpoints:

```
GET /stats   — connection and traffic counters
GET /health  — liveness probe (returns 200 OK)
```

Example output:

```
uptime:        3h42m10s
active_conns:  14
total_conns:   1083
bytes_in:      823442312
bytes_out:     4109273600
secret[alice]: active=9 total=701
secret[bob]:   active=5 total=382
```

## Deploying with systemd

```ini
# /etc/systemd/system/mtproxy.service
[Unit]
Description=MTProxy
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mtproxy
Environment=SECRETS=alice:dd8f3a...
Environment=PORT=443
Environment=STATS_ADDR=:8080
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp mtproxy-linux-amd64 /usr/local/bin/mtproxy
sudo systemctl enable --now mtproxy
sudo journalctl -fu mtproxy
```

## How it works

```
Telegram Client
      │
      │  outer obfuscation (proxy secret, AES-256-CTR)
      ▼
  MTProxy Server
      │
      │  strips outer layer, reads DC ID from inner init
      │  forwards raw Telegram stream
      ▼
  Telegram DC 1–5
```

**Protocol detection** — the server peeks at the first byte of each new connection:

| First byte | Protocol | Secret type |
|------------|----------|-------------|
| `0x16` | Fake-TLS ClientHello | `ee` |
| anything else | Obfuscated2 | `dd` |

**Key derivation** (both protocols):

```
enc_key = SHA256(nonce[8:40] + secret_raw)   ; client→proxy decryption
enc_iv  = nonce[40:56]

dec_key = SHA256(reverse(nonce[8:40]) + secret_raw)  ; proxy→client encryption
dec_iv  = reverse(nonce[40:56])
```

**Fake-TLS** embeds the 64-byte nonce inside the TLS ClientHello:
- `ClientHello.random[0:32]` = `nonce[0:32]`
- `ClientHello.session_id[0:32]` = `nonce[32:64]`

After the fake handshake, all data is wrapped in TLS ApplicationData records (`\x17\x03\x03` + 2-byte length + payload).

## Project structure

```
cmd/mtproxy/main.go   — entry point
config/config.go      — environment-based configuration
dc/list.go            — Telegram DC address table
stats/stats.go        — atomic counters + HTTP server
proxy/obfuscated.go   — dd-protocol (obfuscated2)
proxy/faketls.go      — ee-protocol (fake-TLS)
proxy/server.go       — TCP server, protocol detection, relay
```

## Makefile targets

```bash
make build    # build for current platform
make linux    # static Linux amd64 binary
make secret   # generate a random dd-secret
make clean    # remove binaries
```

## License

MIT
