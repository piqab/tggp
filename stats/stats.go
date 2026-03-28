package stats

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Stats tracks proxy connection and traffic metrics.
type Stats struct {
	activeConns int64
	totalConns  int64
	bytesIn     int64
	bytesOut    int64
	startTime   time.Time

	mu        sync.Mutex
	perSecret map[string]*secretStats
}

type secretStats struct {
	active int64
	total  int64
}

// New creates a new Stats instance.
func New() *Stats {
	return &Stats{
		startTime: time.Now(),
		perSecret: make(map[string]*secretStats),
	}
}

// ConnOpen records a new connection for the given secret name.
func (s *Stats) ConnOpen(secretName string) {
	atomic.AddInt64(&s.activeConns, 1)
	atomic.AddInt64(&s.totalConns, 1)

	s.mu.Lock()
	ss := s.secretStats(secretName)
	s.mu.Unlock()

	atomic.AddInt64(&ss.active, 1)
	atomic.AddInt64(&ss.total, 1)
}

// ConnClose records a closed connection.
func (s *Stats) ConnClose(secretName string) {
	atomic.AddInt64(&s.activeConns, -1)

	s.mu.Lock()
	ss := s.secretStats(secretName)
	s.mu.Unlock()

	atomic.AddInt64(&ss.active, -1)
}

// AddBytesIn records bytes received from clients.
func (s *Stats) AddBytesIn(n int64) { atomic.AddInt64(&s.bytesIn, n) }

// AddBytesOut records bytes sent to clients.
func (s *Stats) AddBytesOut(n int64) { atomic.AddInt64(&s.bytesOut, n) }

// secretStats returns or creates per-secret stats (must be called with mu held).
func (s *Stats) secretStats(name string) *secretStats {
	ss, ok := s.perSecret[name]
	if !ok {
		ss = &secretStats{}
		s.perSecret[name] = ss
	}
	return ss
}

// Serve starts the HTTP stats server on addr (non-blocking).
func (s *Stats) Serve(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "OK")
	})
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			// non-fatal: stats endpoint is optional
		}
	}()
}

func (s *Stats) handleStats(w http.ResponseWriter, _ *http.Request) {
	uptime := time.Since(s.startTime).Round(time.Second)
	fmt.Fprintf(w, "uptime:        %s\n", uptime)
	fmt.Fprintf(w, "active_conns:  %d\n", atomic.LoadInt64(&s.activeConns))
	fmt.Fprintf(w, "total_conns:   %d\n", atomic.LoadInt64(&s.totalConns))
	fmt.Fprintf(w, "bytes_in:      %d\n", atomic.LoadInt64(&s.bytesIn))
	fmt.Fprintf(w, "bytes_out:     %d\n", atomic.LoadInt64(&s.bytesOut))

	s.mu.Lock()
	defer s.mu.Unlock()
	for name, ss := range s.perSecret {
		fmt.Fprintf(w, "secret[%s]: active=%d total=%d\n",
			name,
			atomic.LoadInt64(&ss.active),
			atomic.LoadInt64(&ss.total),
		)
	}
}
