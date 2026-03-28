package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

const (
	socks5Version   = 0x05
	socks5NoAuth    = 0x00
	socks5CmdConnect = 0x01
	socks5AtypIPv4  = 0x01
	socks5AtypDomain = 0x03
	socks5AtypIPv6  = 0x04
)

// handleSOCKS5 completes the SOCKS5 handshake and relays the connection.
// peek contains the first bytes already read from conn (at least the greeting start).
func handleSOCKS5(conn net.Conn, peek []byte) {
	target, err := socks5Handshake(conn, peek)
	if err != nil {
		if !isConnClosedErr(err) {
			log.Printf("socks5 %s: %v", conn.RemoteAddr(), err)
		}
		return
	}

	log.Printf("socks5 %s → %s", conn.RemoteAddr(), target)

	targetConn, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		// Host unreachable
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		log.Printf("socks5 dial %s: %v", target, err)
		return
	}
	defer targetConn.Close()

	// Success response: 05 00 00 01 0.0.0.0:0
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Bidirectional relay.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(targetConn, conn)
		if tc, ok := targetConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(conn, targetConn)
		done <- struct{}{}
	}()
	<-done
}

// socks5Handshake handles the SOCKS5 negotiation and returns the target address.
// peek contains bytes already read (the greeting: version + nmethods + methods).
func socks5Handshake(conn net.Conn, peek []byte) (string, error) {
	if len(peek) < 2 {
		return "", fmt.Errorf("greeting too short")
	}
	if peek[0] != socks5Version {
		return "", fmt.Errorf("unsupported SOCKS version %d", peek[0])
	}

	nMethods := int(peek[1])

	// We may have read some method bytes into peek already.
	haveMethods := len(peek) - 2
	methods := make([]byte, nMethods)
	copy(methods, peek[2:])
	if haveMethods < nMethods {
		if _, err := io.ReadFull(conn, methods[haveMethods:]); err != nil {
			return "", fmt.Errorf("read auth methods: %w", err)
		}
	}

	// Choose no-auth if offered; otherwise reject.
	selected := byte(0xFF)
	for _, m := range methods {
		if m == socks5NoAuth {
			selected = socks5NoAuth
			break
		}
	}
	if _, err := conn.Write([]byte{socks5Version, selected}); err != nil {
		return "", fmt.Errorf("write method: %w", err)
	}
	if selected == 0xFF {
		return "", fmt.Errorf("no acceptable auth method in %v", methods)
	}

	// Read CONNECT request header: ver cmd rsv atyp
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return "", fmt.Errorf("read request header: %w", err)
	}
	if hdr[0] != socks5Version {
		return "", fmt.Errorf("bad request version %d", hdr[0])
	}
	if hdr[1] != socks5CmdConnect {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("unsupported command %d", hdr[1])
	}

	// Read target address.
	var host string
	switch hdr[3] {
	case socks5AtypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", fmt.Errorf("read ipv4: %w", err)
		}
		host = net.IP(b).String()

	case socks5AtypDomain:
		lenB := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenB); err != nil {
			return "", fmt.Errorf("read domain len: %w", err)
		}
		domain := make([]byte, int(lenB[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", fmt.Errorf("read domain: %w", err)
		}
		host = string(domain)

	case socks5AtypIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", fmt.Errorf("read ipv6: %w", err)
		}
		host = "[" + net.IP(b).String() + "]"

	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("unsupported address type %d", hdr[3])
	}

	// Read port (big-endian uint16).
	portB := make([]byte, 2)
	if _, err := io.ReadFull(conn, portB); err != nil {
		return "", fmt.Errorf("read port: %w", err)
	}
	port := binary.BigEndian.Uint16(portB)

	return fmt.Sprintf("%s:%d", host, port), nil
}
