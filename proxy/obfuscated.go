package proxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	"mtproxy/config"
)

// Telegram obfuscated transport protocol markers placed at nonce[56:60].
const (
	protoAbridged     = uint32(0xEFEFEFEF)
	protoIntermediate = uint32(0xDDDDDDDD)
	protoFull         = uint32(0xFEFEFEFE)
)

var errNoMatchingSecret = errors.New("no matching secret")

// obfuscatedConn wraps a net.Conn adding AES-256-CTR obfuscation layers:
//   - Read  decrypts incoming data (client→proxy direction)
//   - Write encrypts outgoing data (proxy→client direction)
type obfuscatedConn struct {
	net.Conn
	dec cipher.Stream // encrypt outgoing (proxy→client)
	enc cipher.Stream // decrypt incoming (client→proxy)
}

func (c *obfuscatedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.enc.XORKeyStream(b[:n], b[:n])
	}
	return n, err
}

func (c *obfuscatedConn) Write(b []byte) (int, error) {
	buf := make([]byte, len(b))
	c.dec.XORKeyStream(buf, b)
	return c.Conn.Write(buf)
}

// newObfuscatedClientConn reads the 64-byte init from conn, tries to match one
// of the provided dd-secrets, and returns:
//   - an obfuscatedConn whose Read/Write apply the proxy cipher layer
//   - the DC ID embedded in the init
//   - the matching secret
//   - the decoded inner 64-byte nonce (to be forwarded to the Telegram DC)
func newObfuscatedClientConn(
	conn net.Conn,
	secrets []config.Secret,
) (net.Conn, int16, *config.Secret, []byte, error) {
	nonce := make([]byte, 64)
	if _, err := io.ReadFull(conn, nonce); err != nil {
		return nil, 0, nil, nil, fmt.Errorf("read nonce: %w", err)
	}

	for i := range secrets {
		s := &secrets[i]
		if s.Type != config.SecretTypeDD {
			continue
		}

		// Forward stream: client uses SHA256(nonce[8:40]+secret) to encrypt sends.
		encKey := sha256.Sum256(concat(nonce[8:40], s.Raw))
		encIV := nonce[40:56]

		block, err := aes.NewCipher(encKey[:])
		if err != nil {
			continue
		}
		encStream := cipher.NewCTR(block, encIV)

		// Decrypt the 64-byte init to read inner nonce.
		inner := make([]byte, 64)
		encStream.XORKeyStream(inner, nonce)

		// Validate protocol tag.
		proto := binary.LittleEndian.Uint32(inner[56:60])
		log.Printf("DBG dd: secret %q proto=0x%08x (want 0xefefefef/0xdddddddd/0xfefefefe)", s.Name, proto)
		if proto != protoAbridged && proto != protoIntermediate && proto != protoFull {
			continue
		}

		// DC ID (signed int16 from int32 LE; media DCs use negative IDs).
		dcID := int16(int32(binary.LittleEndian.Uint32(inner[60:64])))

		// Backward stream: client uses SHA256(reverse(nonce[8:40])+secret) to decrypt receives.
		decKey := sha256.Sum256(concat(reverseBytes(nonce[8:40]), s.Raw))
		decIV := reverseBytes(nonce[40:56])
		decBlock, _ := aes.NewCipher(decKey[:])
		decStream := cipher.NewCTR(decBlock, decIV)
		// Backward stream starts at counter 0; client decrypts from counter 0 as well.

		return &obfuscatedConn{
			Conn: conn,
			enc:  encStream,
			dec:  decStream,
		}, dcID, s, inner, nil
	}

	return nil, 0, nil, nil, errNoMatchingSecret
}

// reverseBytes returns a new slice with bytes in reverse order.
func reverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i, v := range b {
		r[len(b)-1-i] = v
	}
	return r
}

// concat allocates a new slice containing a followed by b.
func concat(a, b []byte) []byte {
	out := make([]byte, len(a)+len(b))
	copy(out, a)
	copy(out[len(a):], b)
	return out
}
