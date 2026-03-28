# MTProxy

MTProto proxy server written in Go. Supports `dd` (obfuscated2), `ee` (fake-TLS), SOCKS5, and Hysteria2 protocols. Multiple concurrent users with per-secret statistics.

## Features

- **dd-secrets** — obfuscated2 transport (AES-256-CTR)
- **ee-secrets** — fake-TLS transport, traffic looks like HTTPS to middleboxes
- **SOCKS5** — standard SOCKS5 proxy (no auth) on the same port as MTProxy or a dedicated port
- **Hysteria2** — QUIC-based proxy on a separate UDP port
- **Multiple secrets** — different secrets for different users, all tracked separately
- **Statistics** — HTTP endpoint with live connection and traffic counters
- **IPv4 only** — all outbound connections use tcp4/udp4
- **Zero stdlib deps** — MTProxy/SOCKS5 use only the Go standard library; Hysteria2 requires `quic-go`

## Requirements

- Go 1.23+ (required by quic-go for Hysteria2; MTProxy/SOCKS5 alone work with 1.22)
- Linux (recommended), macOS or Windows
- Port 443 requires `root` or `CAP_NET_BIND_SERVICE`

### Install Go 1.23 on Linux

```bash
wget https://go.dev/dl/go1.23.8.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.8.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin   # add to ~/.bashrc to persist
```

## Quick Start

```bash
# 1. Clone
git clone https://github.com/yourname/mtproxy
cd mtproxy

# 2. Pull dependencies
go get github.com/quic-go/quic-go@latest
go mod tidy

# 3. Generate a secret
SECRET=$(openssl rand -hex 16)
echo "dd${SECRET}"   # share this with users

# 4. Build
make linux           # static Linux amd64 binary
# or
go build -o mtproxy ./cmd/mtproxy

# 5. Run
SECRETS="myuser:dd${SECRET}" PORT=443 ./mtproxy
```

At startup the server prints a ready-to-use `tg://` link for each secret:

```
[mtproxy] MTProxy listening on 0.0.0.0:443 (1 secrets)
[mtproxy]   [0] myuser  type=dd  link: tg://proxy?server=1.2.3.4&port=443&secret=dd8f3a...
```

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRETS` | *(required)* | Comma-separated list of `[name:]secret` |
| `BIND_ADDR` | `0.0.0.0` | Listen address |
| `PORT` | `443` | MTProxy + SOCKS5 listen port (TCP) |
| `SOCKS5_PORT` | *(disabled)* | Dedicated SOCKS5 port (TCP) |
| `HYSTERIA2_PORT` | *(disabled)* | Hysteria2 port (UDP) |
| `HYSTERIA2_PASSWORD` | *(required if port set)* | Hysteria2 password |
| `HYSTERIA2_CERT` | *(self-signed)* | Path to TLS certificate file |
| `HYSTERIA2_KEY` | *(self-signed)* | Path to TLS key file |
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

### Full example

```bash
export SECRETS="alice:dd8f3a...,bob:ee9d1c...676f6f676c652e636f6d"
export PORT=443
export SOCKS5_PORT=1080
export HYSTERIA2_PORT=8443
export HYSTERIA2_PASSWORD=strongpassword
export STATS_ADDR=":8080"
./mtproxy
```

### Using a .env file

```bash
cp .env.example .env
# edit .env
source .env && ./mtproxy
```

## Protocol details

### MTProxy + SOCKS5 (PORT, TCP)

The server peeks at the first byte of each connection and dispatches:

| First byte | Protocol |
|------------|----------|
| `0x16` | Fake-TLS / ee-secret |
| `0x05` | SOCKS5 (no auth) |
| `0x04` | SOCKS4 (rejected) |
| `G P C D H` | HTTP (rejected) |
| anything else | Obfuscated2 / dd-secret |

SOCKS5 also has its own dedicated port if `SOCKS5_PORT` is set.

### Hysteria2 (HYSTERIA2_PORT, UDP)

- Transport: QUIC over UDP (IPv4 only)
- TLS: TLS 1.3, ALPN `h3`/`hysteria`
- By default a self-signed certificate is generated — clients must enable `insecure: true`
- UDP proxy is not supported (TCP only)

**Client config (`config.yaml`):**
```yaml
server: YOUR_SERVER_IP:8443
auth: strongpassword
tls:
  insecure: true    # omit if using a real certificate
socks5:
  listen: 127.0.0.1:1080
```

### MTProxy key derivation (dd and ee)

```
enc_key = SHA256(nonce[8:40] + secret_raw)   ; client→proxy decryption
enc_iv  = nonce[40:56]

dec_key = SHA256(reverse(nonce[8:40]) + secret_raw)  ; proxy→client encryption
dec_iv  = reverse(nonce[40:56])
```

**Fake-TLS** embeds the 64-byte nonce inside the TLS ClientHello:
- `ClientHello.random[0:32]` = `nonce[0:32]`
- `ClientHello.session_id[0:32]` = `nonce[32:64]`

After the fake handshake all data is wrapped in TLS ApplicationData records (`\x17\x03\x03` + 2-byte length + payload).

## Statistics

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
Environment=SOCKS5_PORT=1080
Environment=HYSTERIA2_PORT=8443
Environment=HYSTERIA2_PASSWORD=strongpassword
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

## Project structure

```
cmd/mtproxy/main.go   — entry point
config/config.go      — environment-based configuration
dc/list.go            — Telegram DC address table
stats/stats.go        — atomic counters + HTTP server
proxy/obfuscated.go   — dd-protocol (obfuscated2)
proxy/faketls.go      — ee-protocol (fake-TLS)
proxy/socks5.go       — SOCKS5 proxy
proxy/hysteria2.go    — Hysteria2 tunnel (QUIC)
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
