// Package hysteria2 provides a gomobile-compatible API for connecting to a
// Hysteria2 proxy server and exposing a local SOCKS5 endpoint on Android.
//
// Build with:
//
//	gomobile bind -target=android -o hysteria2.aar .
package hysteria2

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public API (gomobile exports)
// ─────────────────────────────────────────────────────────────────────────────

// Start connects to the Hysteria2 server and starts a local SOCKS5 proxy.
//
// Parameters:
//   - server        hostname or IP of the Hysteria2 server
//   - port          UDP port (1–65535)
//   - password      authentication password
//   - insecure      skip TLS certificate verification
//   - localSocksPort  TCP port for the local SOCKS5 listener
func Start(server string, port int, password string, insecure bool, localSocksPort int) error {
	return defaultClient.start(server, port, password, insecure, localSocksPort)
}

// Stop disconnects from the server and stops the local SOCKS5 proxy.
func Stop() {
	defaultClient.stop()
}

// IsRunning returns true if the client is currently connected.
func IsRunning() bool {
	return defaultClient.isRunning()
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal client
// ─────────────────────────────────────────────────────────────────────────────

var defaultClient = &hysteria2Client{}

type hysteria2Client struct {
	mu         sync.Mutex
	cancelFunc context.CancelFunc
	running    atomic.Bool
}

func (c *hysteria2Client) isRunning() bool {
	return c.running.Load()
}

func (c *hysteria2Client) start(server string, port int, password string, insecure bool, localSocksPort int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running.Load() {
		return fmt.Errorf("already running")
	}

	addr := fmt.Sprintf("%s:%d", server, port)

	tlsConf := &tls.Config{
		InsecureSkipVerify: insecure, //nolint:gosec // intentional for user-controlled option
		NextProtos:         []string{"h3"},
		MinVersion:         tls.VersionTLS13,
	}

	quicConf := &quic.Config{
		MaxIdleTimeout:        30 * time.Second,
		KeepAlivePeriod:       10 * time.Second,
		MaxIncomingStreams:     -1,
		MaxIncomingUniStreams:  -1,
	}

	ctx, cancel := context.WithCancel(context.Background())

	conn, err := quic.DialAddr(ctx, addr, tlsConf, quicConf)
	if err != nil {
		cancel()
		return fmt.Errorf("QUIC dial %s: %w", addr, err)
	}

	// Hysteria2 auth stream (first stream, no type prefix)
	if err := sendAuth(ctx, conn, password); err != nil {
		cancel()
		conn.CloseWithError(0, "auth failed")
		return fmt.Errorf("auth: %w", err)
	}

	c.cancelFunc = cancel
	c.running.Store(true)

	// Start local SOCKS5 listener
	go func() {
		defer func() {
			c.running.Store(false)
			conn.CloseWithError(0, "stopped")
			log.Println("hysteria2: stopped")
		}()
		if err := runSocks5Listener(ctx, conn, localSocksPort); err != nil {
			log.Printf("hysteria2: SOCKS5 listener error: %v", err)
		}
	}()

	log.Printf("hysteria2: connected to %s, SOCKS5 on :%d", addr, localSocksPort)
	return nil
}

func (c *hysteria2Client) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Hysteria2 auth handshake
// ─────────────────────────────────────────────────────────────────────────────

func sendAuth(ctx context.Context, conn *quic.Conn, password string) error {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open auth stream: %w", err)
	}
	defer stream.Close()

	pwBytes := []byte(password)

	// Client → Server: [uint16 auth_len][auth bytes][uint64 rx=0]
	buf := make([]byte, 2+len(pwBytes)+8)
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(pwBytes)))
	copy(buf[2:], pwBytes)
	binary.BigEndian.PutUint64(buf[2+len(pwBytes):], 0) // rx = 0

	if _, err := stream.Write(buf); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	// Server → Client: [uint8 ok][uint64 rx][uint16 msg_len][msg bytes]
	header := make([]byte, 1+8+2)
	if _, err := io.ReadFull(stream, header); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	ok := header[0]
	msgLen := binary.BigEndian.Uint16(header[9:11])
	if msgLen > 0 {
		msg := make([]byte, msgLen)
		if _, err := io.ReadFull(stream, msg); err != nil {
			return fmt.Errorf("read auth message: %w", err)
		}
		log.Printf("hysteria2: auth server message: %s", string(msg))
	}
	if ok != 1 {
		return fmt.Errorf("server rejected auth (ok=%d)", ok)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Local SOCKS5 server
// ─────────────────────────────────────────────────────────────────────────────

func runSocks5Listener(ctx context.Context, conn *quic.Conn, port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go handleSocks5Client(ctx, c, conn)
	}
}

func handleSocks5Client(ctx context.Context, client net.Conn, conn *quic.Conn) {
	defer client.Close()

	// ── SOCKS5 greeting ──
	buf := make([]byte, 2)
	if _, err := io.ReadFull(client, buf); err != nil {
		return
	}
	if buf[0] != 5 {
		return // Not SOCKS5
	}
	nMethods := int(buf[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	// Respond: no authentication required
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}

	// ── SOCKS5 request ──
	req := make([]byte, 4)
	if _, err := io.ReadFull(client, req); err != nil {
		return
	}
	if req[0] != 5 || req[1] != 1 { // VER=5, CMD=CONNECT
		client.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0}) // command not supported
		return
	}

	var target string
	switch req[3] {
	case 1: // IPv4
		addr := make([]byte, 4)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(client, portBytes); err != nil {
			return
		}
		port := binary.BigEndian.Uint16(portBytes)
		target = fmt.Sprintf("%d.%d.%d.%d:%d", addr[0], addr[1], addr[2], addr[3], port)

	case 3: // Domain name
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(client, lenBuf); err != nil {
			return
		}
		domain := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(client, domain); err != nil {
			return
		}
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(client, portBytes); err != nil {
			return
		}
		port := binary.BigEndian.Uint16(portBytes)
		target = fmt.Sprintf("%s:%d", string(domain), port)

	case 4: // IPv6
		addr := make([]byte, 16)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(client, portBytes); err != nil {
			return
		}
		port := binary.BigEndian.Uint16(portBytes)
		target = fmt.Sprintf("[%s]:%d", net.IP(addr).String(), port)

	default:
		client.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0}) // address type not supported
		return
	}

	// ── Open Hysteria2 TCP proxy stream ──
	stream, err := openTcpStream(ctx, conn, target)
	if err != nil {
		log.Printf("hysteria2: open stream for %s: %v", target, err)
		client.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) // host unreachable
		return
	}
	defer stream.Close()

	// Successful SOCKS5 reply: bound addr 0.0.0.0:0
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	// ── Bidirectional relay ──
	relay(client, stream)
}

// ─────────────────────────────────────────────────────────────────────────────
// Hysteria2 TCP proxy stream
// ─────────────────────────────────────────────────────────────────────────────

func openTcpStream(ctx context.Context, conn *quic.Conn, target string) (*quic.Stream, error) {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open QUIC stream: %w", err)
	}

	addrBytes := []byte(target)

	// [uint8 type=0x01][uint16 addr_len][addr "host:port"][uint32 req_id=0]
	buf := make([]byte, 1+2+len(addrBytes)+4)
	buf[0] = 0x01
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(addrBytes)))
	copy(buf[3:], addrBytes)
	binary.BigEndian.PutUint32(buf[3+len(addrBytes):], 0)

	if _, err := stream.Write(buf); err != nil {
		stream.Close()
		return nil, fmt.Errorf("write TCP request: %w", err)
	}

	// Server reply: [uint8 ok][uint16 msg_len=0]
	resp := make([]byte, 3)
	if _, err := io.ReadFull(stream, resp); err != nil {
		stream.Close()
		return nil, fmt.Errorf("read TCP response: %w", err)
	}
	if resp[0] != 1 {
		msgLen := binary.BigEndian.Uint16(resp[1:3])
		msg := make([]byte, msgLen)
		io.ReadFull(stream, msg) //nolint:errcheck
		stream.Close()
		return nil, fmt.Errorf("server rejected TCP request: %s", string(msg))
	}
	// Consume any message (usually empty)
	msgLen := binary.BigEndian.Uint16(resp[1:3])
	if msgLen > 0 {
		extra := make([]byte, msgLen)
		io.ReadFull(stream, extra) //nolint:errcheck
	}

	return stream, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bidirectional relay helper
// ─────────────────────────────────────────────────────────────────────────────

func relay(a net.Conn, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	copy := func(dst io.Writer, src io.Reader) {
		io.Copy(dst, src) //nolint:errcheck
		done <- struct{}{}
	}
	go copy(a, b)
	go copy(b, a)
	<-done
}
