package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager owns the tunnel: start, stop, auto-restart, trusted_domains, logs.
type Manager struct {
	cfg     Config
	persist bool // remember on/off in DATA_DIR

	mu      sync.Mutex
	url     string
	status  string // stopped | starting | running | error
	cancel  context.CancelFunc
	done    chan struct{}
	logs    []string
	since   time.Time // when the tunnel last came online
	problem string    // plain-words reason the tunnel is not working ("" = fine)
}

type Snapshot struct {
	Version string   `json:"version"`
	Status  string   `json:"status"`
	URL     string   `json:"url"`
	Since   int64    `json:"since"` // unix seconds, 0 when offline
	Problem string   `json:"problem"`
	Logs    []string `json:"logs"`
}

func NewManager(cfg Config, persist bool) *Manager {
	return &Manager{cfg: cfg, persist: persist, status: "stopped"}
}

func (m *Manager) logf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	fmt.Println(line)
	m.mu.Lock()
	m.logs = append(m.logs, line)
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	m.mu.Unlock()
}

func (m *Manager) set(status, url string) {
	m.mu.Lock()
	m.status, m.url = status, url
	if status == "running" {
		if m.since.IsZero() {
			m.since = time.Now()
		}
	} else {
		m.since = time.Time{}
	}
	m.mu.Unlock()
}

func (m *Manager) setProblem(msg string) {
	m.mu.Lock()
	m.problem = msg
	m.mu.Unlock()
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	var since int64
	if !m.since.IsZero() {
		since = m.since.Unix()
	}
	return Snapshot{Version: version, Status: m.status, URL: m.url, Since: since, Problem: m.problem, Logs: append([]string(nil), m.logs...)}
}

func (m *Manager) Start() {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.cancel, m.done, m.status = cancel, done, "starting"
	m.mu.Unlock()
	if m.persist {
		updateSettings(m.cfg, func(s *Settings) { s.Enabled = true })
	}
	go func() {
		m.loop(ctx)
		close(done)
	}()
}

func (m *Manager) stop(remember bool) {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	m.mu.Lock()
	m.cancel, m.done, m.url, m.status, m.since, m.problem = nil, nil, "", "stopped", time.Time{}, ""
	m.mu.Unlock()
	if remember && m.persist {
		updateSettings(m.cfg, func(s *Settings) { s.Enabled = false })
	}
}

func (m *Manager) Stop()     { m.stop(true) }  // user pressed Stop
func (m *Manager) Shutdown() { m.stop(false) } // container is stopping

// Restart applies new settings: it stops a running tunnel (keeping the saved
// on/off choice) and starts it again. Does nothing if the tunnel is stopped.
func (m *Manager) Restart(apply func()) {
	m.mu.Lock()
	wasOn := m.cancel != nil
	m.mu.Unlock()
	if wasOn {
		m.stop(false)
	}
	apply()
	if wasOn {
		m.Start()
	}
}

// wait sleeps for d or until ctx ends. It returns false if ctx ended.
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (m *Manager) loop(ctx context.Context) {
	if m.cfg.Demo {
		m.demoLoop(ctx)
		return
	}
	lastProblem := ""
	for ctx.Err() == nil {
		cfg := effectiveConfig(m.cfg)
		m.set("starting", "")

		// Never start blindly: if Docker or Nextcloud isn't ready, say so and try again later
		// (this also covers the server booting with Nextcloud still starting up).
		if msg := criticalProblem(ctx, cfg); msg != "" {
			if ctx.Err() != nil {
				break
			}
			m.setProblem(msg)
			m.set("error", "")
			if msg != lastProblem {
				m.logf("ERROR setup: %s", msg)
				m.logf("Will check again in 30s. Fix it on the Setup page and the tunnel starts by itself.")
				lastProblem = msg
			}
			if !wait(ctx, 30*time.Second) {
				break
			}
			continue
		}
		lastProblem = ""
		m.setProblem("")

		onURL := func(u string) {
			if ctx.Err() != nil {
				return
			}
			host := strings.TrimPrefix(u, "https://")
			m.logf("Tunnel is up: %s", u)
			if err := setTrusted(cfg, host); err != nil {
				m.logf("ERROR adding trusted domain: %v", err)
				m.setProblem("Nextcloud would not accept the new address: " + friendly(err.Error()))
				m.set("error", "")
				return
			}
			m.logf("Nextcloud now trusts %s", host)
			m.setProblem("")
			m.set("running", u)
		}
		err := runTunnel(ctx, cfg, m.logf, onURL)
		if ctx.Err() != nil {
			break
		}
		m.logf("Tunnel stopped (%v). Restarting in 5s...", err)
		wait(ctx, 5*time.Second)
	}
	clearTrusted(effectiveConfig(m.cfg))
	m.logf("Tunnel stopped, trusted domain removed")
}
