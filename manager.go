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

	mu     sync.Mutex
	url    string
	status string // stopped | starting | running | error
	cancel context.CancelFunc
	done   chan struct{}
	logs   []string
}

type Snapshot struct {
	Version string   `json:"version"`
	Status  string   `json:"status"`
	URL     string   `json:"url"`
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
	m.mu.Unlock()
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{Version: version, Status: m.status, URL: m.url, Logs: append([]string(nil), m.logs...)}
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
	m.cancel, m.done, m.url, m.status = nil, nil, "", "stopped"
	m.mu.Unlock()
	if remember && m.persist {
		updateSettings(m.cfg, func(s *Settings) { s.Enabled = false })
	}
}

func (m *Manager) Stop()     { m.stop(true) }  // user pressed Stop
func (m *Manager) Shutdown() { m.stop(false) } // container is stopping

func (m *Manager) loop(ctx context.Context) {
	onURL := func(u string) {
		if ctx.Err() != nil {
			return
		}
		host := strings.TrimPrefix(u, "https://")
		m.logf("Tunnel is up: %s", u)
		if err := setTrusted(m.cfg, host); err != nil {
			m.logf("ERROR adding trusted domain: %v", err)
			m.set("error", "")
			return
		}
		m.logf("Nextcloud now trusts %s", host)
		m.set("running", u)
	}
	for ctx.Err() == nil {
		m.set("starting", "")
		err := runTunnel(ctx, m.cfg, m.logf, onURL)
		if ctx.Err() != nil {
			break
		}
		m.logf("Tunnel stopped (%v). Restarting in 5s...", err)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	clearTrusted(m.cfg)
	m.logf("Tunnel stopped, trusted domain removed")
}
