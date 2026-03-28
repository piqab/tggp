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

  BIND_ADDR   Listen address (default: 0.0.0.0)
  PORT        Listen port    (default: 443)
  TIMEOUT_SEC Idle timeout   (default: 120)
  STATS_ADDR  HTTP stats     (default: :8080)
`
