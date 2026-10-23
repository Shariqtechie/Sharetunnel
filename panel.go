package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Panel struct {
	cfg      Config
	m        *Manager
	u        *Updater
	password string

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    []time.Time
}

func NewPanel(cfg Config, m *Manager, u *Updater, password string) *Panel {
	return &Panel{cfg: cfg, m: m, u: u, password: password, sessions: map[string]time.Time{}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (p *Panel) valid(r *http.Request) bool {
	c, err := r.Cookie("st_session")
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.sessions[c.Value]
	if ok && time.Now().After(exp) {
		delete(p.sessions, c.Value)
		return false
	}
	return ok
}

// auth = must be logged in. post = must also be a POST.
func (p *Panel) auth(post bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.valid(r) {
			writeJSON(w, 401, map[string]string{"error": "login required"})
			return
		}
		if post && r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "POST only"})
			return
		}
		next(w, r)
	}
}

func (p *Panel) locked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-time.Minute)
	kept := p.fails[:0]
	for _, t := range p.fails {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	p.fails = kept
	return len(p.fails) >= 10
}

func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	if p.locked() {
		writeJSON(w, 429, map[string]string{"error": "too many wrong passwords, wait a minute"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(p.password)) != 1 {
		p.mu.Lock()
		p.fails = append(p.fails, time.Now())
		p.mu.Unlock()
		time.Sleep(time.Second)
		writeJSON(w, 401, map[string]string{"error": "wrong password"})
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	p.mu.Lock()
	p.sessions[tok] = time.Now().Add(7 * 24 * time.Hour)
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "st_session", Value: tok, Path: "/", HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("st_session"); err == nil {
		p.mu.Lock()
		delete(p.sessions, c.Value)
		p.mu.Unlock()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (p *Panel) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { // public, used by the updater
		writeJSON(w, 200, map[string]string{"version": version})
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) { // lets the page ask "am I signed in?" without a 401
		writeJSON(w, 200, map[string]bool{"authenticated": p.valid(r)})
	})
	mux.HandleFunc("/api/login", p.login)
	mux.HandleFunc("/api/logout", p.logout)
	mux.HandleFunc("/api/status", p.auth(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, p.m.Snapshot())
	}))
	mux.HandleFunc("/api/shares", p.auth(false, func(w http.ResponseWriter, r *http.Request) {
		shares, err := listShares(p.cfg)
		out := map[string]any{"shares": shares}
		if err != nil {
			out["shares"] = []ShareOut{}
			out["error"] = err.Error()
		}
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("/api/start", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		p.m.Start()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/stop", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		p.m.Stop()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/update", p.auth(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, p.u.Info())
	}))
	mux.HandleFunc("/api/update/check", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		p.u.Check()
		writeJSON(w, 200, p.u.Info())
	}))
	mux.HandleFunc("/api/update/apply", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		if err := p.u.Apply(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/update/settings", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
			Hour int    `json:"hour"`
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
		if (body.Mode != "notify" && body.Mode != "auto" && body.Mode != "off") || body.Hour < 0 || body.Hour > 23 {
			writeJSON(w, 400, map[string]string{"error": "invalid settings"})
			return
		}
		updateSettings(p.cfg, func(s *Settings) { s.UpdateMode, s.AutoHour = body.Mode, body.Hour })
		writeJSON(w, 200, p.u.Info())
	}))
	return secure(mux)
}

// secure adds standard browser protections to every response.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
