package proxy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

const hy2StreamTypeTCP = 0x01

// ListenHysteria2 starts a Hysteria2-compatible QUIC proxy server on UDP.
// If certFile/keyFile are empty, a self-signed certificate is generated.
func ListenHysteria2(bindAddr string, port int, password, certFile, keyFile string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, port)

	var (
		tlsCert tls.Certificate
		err     error
	)
	if certFile != "" && keyFile != "" {
		tlsCert, err = tls.LoadX509KeyPair(certFile, keyFile)
	} else {
		tlsCert, err = generateSelfSignedCert()
	}
	if err != nil {
		return fmt.Errorf("hysteria2 tls: %w", err)
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"h3", "hysteria"},
		MinVersion:   tls.VersionTLS13,
	}

	// Bind to udp4 explicitly (no IPv6).
	udpConn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("hysteria2 udp listen: %w", err)
	}

	tr := &quic.Transport{Conn: udpConn}
	ln, err := tr.Listen(tlsConf, &quic.Config{
		MaxIdleTimeout:       120 * time.Second,
		MaxIncomingStreams:    1024,
		MaxIncomingUniStreams: -1,
		EnableDatagrams:      false,
	})
	if err != nil {
		return fmt.Errorf("hysteria2 listen: %w", err)
	}
	defer ln.Close()

	log.Printf("Hysteria2 listening on %s (UDP)", addr)

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Printf("hysteria2 accept: %v", err)
			continue
		}
		go hy2HandleConn(conn, password)
	}
}

func hy2HandleConn(conn quic.Connection, password string) {
	defer conn.CloseWithError(0, "done")
	ctx := context.Background()

	// The first client-initiated stream is the auth stream (no type prefix).
	authStream, err := conn.AcceptStream(ctx)
	if err != nil {
		return
	}
	authed, err := hy2HandleAuth(authStream, password)
	authStream.Close()
	if err != nil {
		log.Printf("hysteria2 auth from %s: %v", conn.RemoteAddr(), err)
		conn.CloseWithError(1, "auth error")
		return
	}
	if !authed {
		log.Printf("hysteria2 auth rejected: %s", conn.RemoteAddr())
		conn.CloseWithError(1, "auth failed")
		return
	}
	log.Printf("hysteria2 authed: %s", conn.RemoteAddr())

	// Accept proxy streams.
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		go hy2HandleStream(stream, conn.RemoteAddr())
	}
}

// hy2HandleAuth reads ClientHello and writes ServerHello on the auth stream.
//
// ClientHello: [uint16 auth_len][auth][uint64 rx_bps]
// ServerHello: [uint8 ok][uint64 rx][uint16 msg_len][msg]
func hy2HandleAuth(stream quic.Stream, password string) (bool, error) {
	auth, err := hy2ReadStr(stream)
	if err != nil {
		return false, fmt.Errorf("read auth: %w", err)
	}
	var rxBuf [8]byte
	if _, err := io.ReadFull(stream, rxBuf[:]); err != nil {
		return false, fmt.Errorf("read rx: %w", err)
	}

	ok := auth == password

	resp := make([]byte, 0, 16)
	if ok {
		resp = append(resp, 0x01)
	} else {
		resp = append(resp, 0x00)
	}
	resp = binary.BigEndian.AppendUint64(resp, 0) // rx = unlimited
	msg := ""
	if !ok {
		msg = "auth failed"
	}
	resp = hy2AppendStr(resp, msg)
	_, _ = stream.Write(resp)

	return ok, nil
}

// hy2HandleStream reads the type byte and dispatches to the right handler.
//
// Proxy stream format: [uint8 type][...]
func hy2HandleStream(stream quic.Stream, remoteAddr net.Addr) {
	defer stream.Close()

	var typeBuf [1]byte
	if _, err := io.ReadFull(stream, typeBuf[:]); err != nil {
		return
	}
	switch typeBuf[0] {
	case hy2StreamTypeTCP:
		hy2HandleTCP(stream, remoteAddr)
	default:
		log.Printf("hysteria2 unknown stream type 0x%02x from %s", typeBuf[0], remoteAddr)
	}
}

// hy2HandleTCP handles a TCP proxy stream.
//
// TCPRequest:  [uint16 addr_len][addr][uint32 req_id]
// TCPResponse: [uint8 ok][uint16 msg_len][msg]
func hy2HandleTCP(stream quic.Stream, remoteAddr net.Addr) {
	addr, err := hy2ReadStr(stream)
	if err != nil {
		return
	}
	var reqID [4]byte // req_id used by clients for logging; ignored here
	if _, err := io.ReadFull(stream, reqID[:]); err != nil {
		return
	}

	target, err := net.DialTimeout("tcp4", addr, 15*time.Second)
	if err != nil {
		resp := []byte{0x00}
		resp = hy2AppendStr(resp, err.Error())
		stream.Write(resp)
		log.Printf("hysteria2 dial %s: %v", addr, err)
		return
	}
	defer target.Close()

	// Send success response.
	resp := []byte{0x01}
	resp = hy2AppendStr(resp, "")
	if _, err := stream.Write(resp); err != nil {
		return
	}

	log.Printf("hysteria2 %s → %s", remoteAddr, addr)

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, stream)
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(stream, target)
		done <- struct{}{}
	}()
	<-done
}

// hy2ReadStr reads a uint16-length-prefixed string.
func hy2ReadStr(r io.Reader) (string, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	if n == 0 {
		return "", nil
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", err
	}
	return string(data), nil
}

// hy2AppendStr appends a uint16-length-prefixed string to b.
func hy2AppendStr(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

// generateSelfSignedCert creates a self-signed TLS certificate.
func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hysteria2"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return tls.X509KeyPair(certPEM, keyPEM)
}
