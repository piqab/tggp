package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type SecretType int

const (
	SecretTypeDD SecretType = iota // dd-prefix or plain 16-byte hex
	SecretTypeEE                   // ee-prefix + domain (fake-TLS)
)

type Secret struct {
	Raw    []byte
	Type   SecretType
	Domain string // for ee-type: SNI domain
	Name   string // human-readable label for stats
}

// HexString returns the full secret string for sharing with clients.
func (s *Secret) HexString() string {
	switch s.Type {
	case SecretTypeDD:
		return "dd" + hex.EncodeToString(s.Raw)
	case SecretTypeEE:
		return "ee" + hex.EncodeToString(s.Raw) + hex.EncodeToString([]byte(s.Domain))
	}
	return hex.EncodeToString(s.Raw)
}

type Config struct {
	BindAddr   string
	Port       int
	Socks5Port int
	Secrets    []Secret
	Timeout    time.Duration
	StatsAddr  string
}

// Load reads configuration from environment variables.
//
// Required:
//   SECRETS  — comma-separated list of [name:]secret
//
// Optional:
//   BIND_ADDR   — listen address (default: 0.0.0.0)
//   PORT        — listen port (default: 443)
//   TIMEOUT_SEC — idle timeout seconds (default: 120)
//   STATS_ADDR  — stats HTTP addr (default: :8080)
func Load() (*Config, error) {
	cfg := &Config{
		BindAddr:   envStr("BIND_ADDR", "0.0.0.0"),
		Port:       envInt("PORT", 443),
		Socks5Port: envInt("SOCKS5_PORT", 0),
		Timeout:    time.Duration(envInt("TIMEOUT_SEC", 120)) * time.Second,
		StatsAddr:  envStr("STATS_ADDR", ":8080"),
	}

	raw := os.Getenv("SECRETS")
	if raw == "" {
		return nil, fmt.Errorf("SECRETS env var is required (format: [name:]secret[,...])")
	}

	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		name, secretStr := "", part
		if idx := strings.IndexByte(part, ':'); idx >= 0 {
			name = part[:idx]
			secretStr = part[idx+1:]
		}

		sec, err := parseSecret(strings.ToLower(secretStr))
		if err != nil {
			return nil, fmt.Errorf("invalid secret %q: %w", part, err)
		}
		if name == "" {
			name = secretStr[:min(8, len(secretStr))]
		}
		sec.Name = name
		cfg.Secrets = append(cfg.Secrets, sec)
	}

	if len(cfg.Secrets) == 0 {
		return nil, fmt.Errorf("no valid secrets in SECRETS")
	}

	return cfg, nil
}

// parseSecret parses a hex secret string into a Secret struct.
// Formats:
//   eeXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX[domain_hex]  — fake-TLS
//   ddXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX               — obfuscated2
//   XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX                   — plain 16-byte hex (treated as dd)
func parseSecret(s string) (Secret, error) {
	if strings.HasPrefix(s, "ee") {
		rest := s[2:]
		if len(rest) < 32 {
			return Secret{}, fmt.Errorf("ee secret too short (need at least 32 hex chars after 'ee')")
		}
		raw, err := hex.DecodeString(rest[:32])
		if err != nil {
			return Secret{}, fmt.Errorf("invalid hex in ee secret: %w", err)
		}
		domain := ""
		if len(rest) > 32 {
			domainBytes, err := hex.DecodeString(rest[32:])
			if err != nil {
				return Secret{}, fmt.Errorf("invalid domain hex: %w", err)
			}
			domain = string(domainBytes)
		}
		return Secret{Raw: raw, Type: SecretTypeEE, Domain: domain}, nil
	}

	if strings.HasPrefix(s, "dd") {
		raw, err := hex.DecodeString(s[2:])
		if err != nil {
			return Secret{}, fmt.Errorf("invalid hex in dd secret: %w", err)
		}
		if len(raw) != 16 {
			return Secret{}, fmt.Errorf("dd secret raw part must be 16 bytes, got %d", len(raw))
		}
		return Secret{Raw: raw, Type: SecretTypeDD}, nil
	}

	raw, err := hex.DecodeString(s)
	if err != nil {
		return Secret{}, fmt.Errorf("invalid hex: %w", err)
	}
	if len(raw) != 16 {
		return Secret{}, fmt.Errorf("plain secret must be 16 bytes hex (32 chars), got %d bytes", len(raw))
	}
	return Secret{Raw: raw, Type: SecretTypeDD}, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
