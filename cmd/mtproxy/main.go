package main

import (
	"log"
	"os"

	"mtproxy/config"
	"mtproxy/dc"
	"mtproxy/proxy"
	"mtproxy/stats"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetPrefix("[mtproxy] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v\n\nSet env vars:\n%s", err, usage)
	}

	st := stats.New()
	st.Serve(cfg.StatsAddr)
	log.Printf("Stats endpoint: http://%s/stats", cfg.StatsAddr)

	if cfg.Hysteria2Port != 0 {
		if cfg.Hysteria2Password == "" {
			log.Fatal("HYSTERIA2_PASSWORD is required when HYSTERIA2_PORT is set")
		}
		go func() {
			if err := proxy.ListenHysteria2(
				cfg.BindAddr, cfg.Hysteria2Port,
				cfg.Hysteria2Password,
				cfg.Hysteria2CertFile, cfg.Hysteria2KeyFile,
			); err != nil {
				log.Fatalf("hysteria2: %v", err)
			}
		}()
	}

	dcList := dc.New()

	srv := proxy.New(cfg, dcList, st)
	if err := srv.Listen(); err != nil {
		log.Fatalf("server: %v", err)
		os.Exit(1)
	}
}

const usage = `
  SECRETS     (required) Comma-separated list of secrets:
                [name:]<secret>  where secret is one of:
                  <32 hex chars>                 — plain/dd secret
                  dd<32 hex chars>               — explicit dd secret
                  ee<32 hex chars>[<domain hex>] — fake-TLS secret

              Examples:
                SECRETS=alice:dd0102030405060708090a0b0c0d0e0f
                SECRETS=alice:dd...,bob:ee...676f6f676c652e636f6d

  BIND_ADDR          Listen address             (default: 0.0.0.0)
  PORT               MTProxy listen port        (default: 443)
  SOCKS5_PORT        SOCKS5 listen port         (default: disabled)
  HYSTERIA2_PORT     Hysteria2 UDP port         (default: disabled)
  HYSTERIA2_PASSWORD Hysteria2 password         (required if HYSTERIA2_PORT set)
  HYSTERIA2_CERT     TLS cert file for H2       (default: self-signed)
  HYSTERIA2_KEY      TLS key file for H2        (default: self-signed)
  TIMEOUT_SEC        Idle timeout               (default: 120)
  STATS_ADDR         HTTP stats                 (default: :8080)
`
