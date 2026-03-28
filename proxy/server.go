package proxy

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"log"
	"net"
	"time"

	"mtproxy/config"
	"mtproxy/dc"
	"mtproxy/stats"
)

// Server is the MTProxy TCP server.
type Server struct {
	cfg    *config.Config
	dcList *dc.List
	stats  *stats.Stats
}

// New creates a new Server.
func New(cfg *config.Config, dcList *dc.List, st *stats.Stats) *Server {
	return &Server{cfg: cfg, dcList: dcList, stats: st}
}

// Listen binds and serves incoming connections. Blocks until error.
func (s *Server) Listen() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.BindAddr, s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer ln.Close()

	log.Printf("MTProxy listening on %s (%d secrets)", addr, len(s.cfg.Secrets))
	for i, sec := range s.cfg.Secrets {
		log.Printf("  [%d] %s  type=%s  link: tg://proxy?server=HOST&port=%d&secret=%s",
			i, sec.Name, secretTypeName(sec.Type), s.cfg.Port, sec.HexString())
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(rawConn net.Conn) {
	defer rawConn.Close()
	rawConn.SetDeadline(time.Now().Add(s.cfg.Timeout))

	log.Printf("DBG new conn from %s", rawConn.RemoteAddr())

	// Read first 4 bytes to detect protocol and log for diagnostics.
	peek := make([]byte, 4)
	n, err := io.ReadAtLeast(rawConn, peek, 1)
	if err != nil {
		log.Printf("DBG peek error from %s: %v", rawConn.RemoteAddr(), err)
		return
	}
	peek = peek[:n]
	log.Printf("DBG peek %d bytes from %s: % 02x", n, rawConn.RemoteAddr(), peek)

	// prependConn re-inserts the peeked bytes into the read stream.
	pconn := &prependConn{Conn: rawConn, buf: append([]byte(nil), peek...)}

	var (
		clientConn net.Conn
		dcID       int16
		secretUsed *config.Secret
		innerNonce []byte
		err        error
	)

	if peek[0] == 0x16 {
		log.Printf("DBG detected fake-TLS (ee) from %s", rawConn.RemoteAddr())
		clientConn, dcID, secretUsed, innerNonce, err = newFakeTLSClientConn(pconn, s.cfg.Secrets)
	} else {
		log.Printf("DBG detected obfuscated (dd) from %s", rawConn.RemoteAddr())
		clientConn, dcID, secretUsed, innerNonce, err = newObfuscatedClientConn(pconn, s.cfg.Secrets)
	}

	if err != nil {
		log.Printf("DBG handshake error from %s: %v (suppressed=%v)",
			rawConn.RemoteAddr(), err, isConnClosedErr(err))
		return
	}
	log.Printf("DBG handshake OK from %s: DC=%d secret=%s", rawConn.RemoteAddr(), dcID, secretUsed.Name)

	// Connect to the Telegram DC.
	dcAddr, err := s.dcList.Get(dcID)
	if err != nil {
		log.Printf("DC %d from %s: %v", dcID, rawConn.RemoteAddr(), err)
		return
	}

	dcConn, err := net.DialTimeout("tcp", dcAddr, 15*time.Second)
	if err != nil {
		log.Printf("connect DC%d %s: %v", dcID, dcAddr, err)
		return
	}
	defer dcConn.Close()
	log.Printf("connected DC%d (%s) for %s", dcID, dcAddr, rawConn.RemoteAddr())

	// Forward the decoded inner 64-byte Telegram init to the DC.
	if _, err := dcConn.Write(innerNonce); err != nil {
		log.Printf("write init to DC%d: %v", dcID, err)
		return
	}

	// Remove connection deadline for the relay phase.
	rawConn.SetDeadline(time.Time{})

	s.stats.ConnOpen(secretUsed.Name)
	defer s.stats.ConnClose(secretUsed.Name)

	log.Printf("relay  %s → DC%d (%s)  secret=%s",
		rawConn.RemoteAddr(), dcID, dcAddr, secretUsed.Name)

	// Bidirectional relay.
	done := make(chan struct{}, 2)

	go func() {
		n, err := io.Copy(dcConn, clientConn)
		s.stats.AddBytesIn(n)
		if err != nil && !isConnClosedErr(err) {
			log.Printf("relay client→DC%d: %v", dcID, err)
		}
		if tc, ok := dcConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		n, err := io.Copy(clientConn, dcConn)
		s.stats.AddBytesOut(n)
		if err != nil && !isConnClosedErr(err) {
			log.Printf("relay DC%d→client: %v", dcID, err)
		}
		done <- struct{}{}
	}()

	<-done
}

// prependConn is a net.Conn that prepends already-read bytes before returning
// data from the underlying connection.
type prependConn struct {
	net.Conn
	buf []byte
}

func (p *prependConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// isConnClosedErr reports whether err is a routine connection-closed condition
// (EOF, unexpected EOF, connection reset) that should not be logged as an error.
func isConnClosedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "use of closed network connection")
}

func secretTypeName(t config.SecretType) string {
	switch t {
	case config.SecretTypeDD:
		return "dd"
	case config.SecretTypeEE:
		return "ee (fake-TLS)"
	}
	return "unknown"
}
