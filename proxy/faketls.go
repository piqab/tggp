package proxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"mtproxy/config"
)

// TLS content types.
const (
	tlsHandshake        = 0x16
	tlsChangeCipherSpec = 0x14
	tlsApplicationData  = 0x17
)

// fakeTLSConn wraps a net.Conn adding:
//   - fake TLS ApplicationData framing on writes
//   - TLS ApplicationData record stripping on reads
//   - AES-256-CTR obfuscation on payload in both directions
type fakeTLSConn struct {
	net.Conn
	dec     cipher.Stream // encrypt outgoing (proxy→client)
	enc     cipher.Stream // decrypt incoming (client→proxy)
	readBuf []byte        // leftover decrypted bytes from last record
}

func (c *fakeTLSConn) Read(b []byte) (int, error) {
	// Return any buffered bytes first.
	if len(c.readBuf) > 0 {
		n := copy(b, c.readBuf)
		c.readBuf = c.readBuf[n:]
		return n, nil
	}

	// Read one TLS record.
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(c.Conn, hdr); err != nil {
		return 0, err
	}
	if hdr[0] != tlsApplicationData {
		return 0, errors.New("faketls: expected ApplicationData record")
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen == 0 {
		return 0, nil
	}
	payload := make([]byte, recLen)
	if _, err := io.ReadFull(c.Conn, payload); err != nil {
		return 0, err
	}

	// Decrypt the proxy obfuscation layer.
	c.enc.XORKeyStream(payload, payload)

	n := copy(b, payload)
	if n < len(payload) {
		c.readBuf = payload[n:]
	}
	return n, nil
}

func (c *fakeTLSConn) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		chunk := b
		const maxRecord = 16384
		if len(chunk) > maxRecord {
			chunk = chunk[:maxRecord]
		}

		// Encrypt the proxy obfuscation layer.
		enc := make([]byte, len(chunk))
		c.dec.XORKeyStream(enc, chunk)

		// Build TLS ApplicationData record.
		record := make([]byte, 5+len(enc))
		record[0] = tlsApplicationData
		record[1] = 0x03
		record[2] = 0x03
		binary.BigEndian.PutUint16(record[3:5], uint16(len(enc)))
		copy(record[5:], enc)

		if _, err := c.Conn.Write(record); err != nil {
			return total, err
		}
		total += len(chunk)
		b = b[len(chunk):]
	}
	return total, nil
}

// newFakeTLSClientConn handles the fake-TLS handshake (ee-secret).
// conn must have the first byte (0x16) already returned to its read buffer
// (i.e. passed as a prependConn). readClientHello reads the full 5-byte header.
func newFakeTLSClientConn(
	conn net.Conn,
	secrets []config.Secret,
) (net.Conn, int16, *config.Secret, []byte, error) {
	nonce, sni, err := readClientHello(conn)
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("faketls: %w", err)
	}

	for i := range secrets {
		s := &secrets[i]
		if s.Type != config.SecretTypeEE {
			continue
		}

		// If a domain is configured, the SNI must match.
		if s.Domain != "" && !strings.EqualFold(sni, s.Domain) {
			continue
		}

		encKey := sha256.Sum256(concat(nonce[8:40], s.Raw))
		encIV := nonce[40:56]

		block, err := aes.NewCipher(encKey[:])
		if err != nil {
			continue
		}
		encStream := cipher.NewCTR(block, encIV)

		inner := make([]byte, 64)
		encStream.XORKeyStream(inner, nonce)

		proto := binary.LittleEndian.Uint32(inner[56:60])
		if proto != protoAbridged && proto != protoIntermediate && proto != protoFull {
			continue
		}

		dcID := int16(int32(binary.LittleEndian.Uint32(inner[60:64])))

		decKey := sha256.Sum256(concat(reverseBytes(nonce[8:40]), s.Raw))
		decIV := reverseBytes(nonce[40:56])
		decBlock, _ := aes.NewCipher(decKey[:])
		decStream := cipher.NewCTR(decBlock, decIV)

		// Send fake TLS ServerHello before handing off the connection.
		if err := sendFakeTLSServerHello(conn); err != nil {
			return nil, 0, nil, nil, err
		}

		return &fakeTLSConn{
			Conn: conn,
			enc:  encStream,
			dec:  decStream,
		}, dcID, s, inner, nil
	}

	return nil, 0, nil, nil, errNoMatchingSecret
}

// readClientHello reads a full TLS ClientHello record (including the 5-byte
// record header) and extracts:
//   - the 64-byte embedded nonce  (random[0:32] || session_id[0:32])
//   - the SNI hostname
func readClientHello(conn net.Conn) (nonce []byte, sni string, err error) {
	// Full 5-byte TLS record header: content_type(1) + version(2) + length(2)
	hdr := make([]byte, 5)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return
	}
	if hdr[0] != tlsHandshake {
		err = errors.New("not a TLS handshake record")
		return
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))

	rec := make([]byte, recLen)
	if _, err = io.ReadFull(conn, rec); err != nil {
		return
	}

	// Handshake header: type(1) + length(3)
	if len(rec) < 4 || rec[0] != 0x01 {
		err = errors.New("expected ClientHello handshake type")
		return
	}

	pos := 4 // skip handshake header

	// ClientHello.legacy_version (2 bytes)
	if pos+2 > len(rec) {
		err = errors.New("truncated at legacy_version")
		return
	}
	pos += 2

	// ClientHello.random (32 bytes) → nonce[0:32]
	if pos+32 > len(rec) {
		err = errors.New("truncated at random")
		return
	}
	random := rec[pos : pos+32]
	pos += 32

	// session_id_len (1 byte)
	if pos >= len(rec) {
		err = errors.New("truncated at session_id_len")
		return
	}
	sessLen := int(rec[pos])
	pos++
	if sessLen != 32 || pos+sessLen > len(rec) {
		err = errors.New("session_id length must be 32")
		return
	}
	sessionID := rec[pos : pos+32]
	pos += sessLen

	nonce = make([]byte, 64)
	copy(nonce[0:32], random)
	copy(nonce[32:64], sessionID)

	// cipher_suites_len (2 bytes) + data
	if pos+2 > len(rec) {
		return
	}
	csLen := int(binary.BigEndian.Uint16(rec[pos : pos+2]))
	pos += 2 + csLen

	// compression_methods_len (1 byte) + data
	if pos+1 > len(rec) {
		return
	}
	pos += 1 + int(rec[pos])
	pos++

	// extensions_len (2 bytes)
	if pos+2 > len(rec) {
		return
	}
	extTotalLen := int(binary.BigEndian.Uint16(rec[pos : pos+2]))
	pos += 2
	extEnd := pos + extTotalLen

	// Walk extensions looking for SNI (type 0x0000).
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(rec[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(rec[pos+2 : pos+4]))
		pos += 4

		if extType == 0x0000 && extLen > 5 && pos+extLen <= extEnd {
			// server_name_list_length (2) + name_type (1) + name_length (2) + name
			nameLen := int(binary.BigEndian.Uint16(rec[pos+3 : pos+5]))
			if pos+5+nameLen <= extEnd {
				sni = string(rec[pos+5 : pos+5+nameLen])
			}
		}
		pos += extLen
	}
	return
}

// sendFakeTLSServerHello sends a fake TLS 1.3 ServerHello + ChangeCipherSpec.
func sendFakeTLSServerHello(conn net.Conn) error {
	serverRandom := make([]byte, 32)
	if _, err := rand.Read(serverRandom); err != nil {
		return err
	}
	sessID := make([]byte, 32)
	if _, err := rand.Read(sessID); err != nil {
		return err
	}

	// ServerHello body
	body := []byte{}
	body = append(body, 0x03, 0x03)         // legacy_version = TLS 1.2
	body = append(body, serverRandom...)     // server_random
	body = append(body, byte(len(sessID)))  // session_id_len
	body = append(body, sessID...)           // session_id
	body = append(body, 0x13, 0x01)         // cipher_suite: TLS_AES_128_GCM_SHA256
	body = append(body, 0x00)               // compression: null

	// Extensions: supported_versions = TLS 1.3
	ext := []byte{0x00, 0x2b, 0x00, 0x02, 0x03, 0x04}
	body = append(body, 0x00, byte(len(ext)))
	body = append(body, ext...)

	// Handshake wrapper
	hs := []byte{0x02, 0x00, byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)

	// TLS record for ServerHello
	shRecord := tlsRecord(tlsHandshake, 0x03, 0x03, hs)

	// ChangeCipherSpec record
	ccsRecord := []byte{tlsChangeCipherSpec, 0x03, 0x03, 0x00, 0x01, 0x01}

	out := append(shRecord, ccsRecord...)
	_, err := conn.Write(out)
	return err
}

func tlsRecord(contentType, v1, v2 byte, payload []byte) []byte {
	r := make([]byte, 5+len(payload))
	r[0] = contentType
	r[1] = v1
	r[2] = v2
	binary.BigEndian.PutUint16(r[3:5], uint16(len(payload)))
	copy(r[5:], payload)
	return r
}
