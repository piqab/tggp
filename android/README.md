# Hysteria2 Proxy — Android

Android VPN client for connecting to the [mtproxy](../README.md) server via Hysteria2 (QUIC/UDP).
All device traffic is routed through an encrypted tunnel.

## How it works

```
Device apps
    ↓ (all TCP/IP traffic)
Android VpnService  ──  TUN interface (10.0.0.1/24)
    ↓
TunWorker  ──  parses IPv4/TCP packets  →  local SOCKS5 (127.0.0.1:10808)
    ↓
Go hysteria2 client  ──  one QUIC stream per TCP connection
    ↓
Hysteria2 server  ──  YOUR_SERVER:HYSTERIA2_PORT (UDP)
    ↓
Internet
```

1. `VpnService` creates a TUN interface and intercepts all IP traffic
2. `TunWorker` reads TCP packets and opens a SOCKS5 session for each flow
3. The Go library (built with gomobile) implements the Hysteria2 protocol over QUIC
4. Each TCP connection becomes a separate QUIC stream to the server

## Project structure

```
android/
├── go/
│   ├── hysteria2.go        — Go package: QUIC client + local SOCKS5 server
│   ├── go.mod              — depends on quic-go v0.59.0
│   └── build.sh            — gomobile bind → hysteria2.aar
└── app/
    ├── build.gradle.kts
    ├── libs/               — place hysteria2.aar here after building
    └── src/main/
        ├── AndroidManifest.xml
        └── kotlin/com/proxy/hysteria/
            ├── App.kt              — Application subclass, notification channel
            ├── Config.kt           — SharedPreferences (server/port/password)
            ├── MainActivity.kt     — UI: input fields, connect button, status
            ├── ProxyVpnService.kt  — VpnService: TUN device + Go client
            └── TunWorker.kt        — tun2socks: IPv4/TCP → SOCKS5
```

## Build requirements

- **Go 1.23+** (required by quic-go v0.59)
- **Android NDK** r25+
- **gomobile** (`go install golang.org/x/mobile/cmd/gomobile@latest`)
- **Android Studio** Hedgehog (2023.1.1) or newer
- **JDK 17**

## Build

### Step 1 — Build the Go AAR

#### Linux / macOS

```bash
cd android/go

# Install gomobile (once)
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init

# Build
./build.sh
```

#### Windows

```bat
cd android\go

:: Install gomobile (once)
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init

:: Build
build.bat
```

The script auto-detects the Android NDK from `%LOCALAPPDATA%\Android\Sdk\ndk\`.
If detection fails, set the path manually before running:

```bat
set ANDROID_NDK_HOME=C:\Users\%USERNAME%\AppData\Local\Android\Sdk\ndk\26.3.11579264
build.bat
```

Output: `android/app/libs/hysteria2.aar`

> **Install NDK**: Android Studio → **SDK Manager → SDK Tools → NDK (Side by side)**

### Step 2 — Build the Android app

Open `android/` in Android Studio, sync Gradle, then:

**Build → Build Bundle(s) / APK(s) → Build APK(s)**

Or from the command line:

```bash
# Linux / macOS
cd android && ./gradlew assembleDebug

# Windows
cd android && gradlew.bat assembleDebug
```

Output: `app/build/outputs/apk/debug/app-debug.apk`

## Server configuration

Start [mtproxy](../README.md) with Hysteria2 enabled before using the app:

```bash
SECRETS="alice:dd..."         \
PORT=443                      \
HYSTERIA2_PORT=8443           \
HYSTERIA2_PASSWORD=mypassword \
./mtproxy
```

## Usage

1. Install the APK on your device
2. Enter connection details:
   - **Server** — server IP or hostname
   - **Port** — Hysteria2 UDP port (default `8443`)
   - **Password** — value of `HYSTERIA2_PASSWORD` from the server config
   - **Skip TLS verify** — enable if the server uses a self-signed certificate
3. Tap **Connect**
4. Accept the VPN permission prompt
5. Status changes to **Connected** — all traffic now goes through the tunnel

## Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `QUIC dial: timeout` | Server unreachable | Check IP/port and firewall (UDP must be open) |
| `auth: server rejected` | Wrong password | Check `HYSTERIA2_PASSWORD` on the server |
| `QUIC dial: tls: failed` | Certificate mismatch | Enable **Skip TLS verify** |
| VPN won't connect | Permission not granted | Check Android VPN settings |
| `listen :10808: address in use` | Port conflict | Restart the app |

### Logcat

```bash
# Go client logs
adb logcat -s hysteria2

# VPN service logs
adb logcat -s ProxyVpnService TunWorker
```

## Protocol

Hysteria2 runs a custom application protocol over TLS 1.3 + QUIC (UDP).

**Auth stream** (first stream opened by client, no type prefix):
```
Client → Server:  [uint16 len][password bytes][uint64 rx=0]
Server → Client:  [uint8 ok=1][uint64 rx][uint16 msg_len][msg]
```

**TCP proxy stream** (one per connection):
```
Client → Server:  [uint8 type=0x01][uint16 addr_len][host:port][uint32 req_id=0]
Server → Client:  [uint8 ok=1][uint16 msg_len=0]
→ bidirectional relay
```

## Security notes

- TLS 1.3 minimum, ALPN `h3` (traffic resembles HTTP/3)
- For production use, set a real TLS certificate on the server (`HYSTERIA2_CERT` / `HYSTERIA2_KEY`)
  and disable **Skip TLS verify** in the app

## License

MIT
