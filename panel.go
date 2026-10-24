package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Panel struct {
	cfg   Config
	m     *Manager
	u     *Updater
	creds *Auth

	mu       sync.Mutex
	sessions map[string]session
	fails    []time.Time

	repMu sync.Mutex // guards the cached setup report
	rep   Report
	repAt time.Time
}

// A session remembers which password it was created under, so changing or
// resetting the password signs everyone out.
type session struct {
	exp time.Time
	fp  string
}

func NewPanel(cfg Config, m *Manager, u *Updater, envPassword string) *Panel {
	return &Panel{cfg: cfg, m: m, u: u, creds: NewAuth(cfg, envPassword), sessions: map[string]session{}}
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
	fp := p.creds.Fingerprint()
	p.mu.Lock()
	defer p.mu.Unlock()
	ss, ok := p.sessions[c.Value]
	if ok && (fp == "" || ss.fp != fp || time.Now().After(ss.exp)) {
		delete(p.sessions, c.Value)
		return false
	}
	return ok
}

// startSession signs the browser in.
func (p *Panel) startSession(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	p.mu.Lock()
	p.sessions[tok] = session{exp: time.Now().Add(7 * 24 * time.Hour), fp: p.creds.Fingerprint()}
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "st_session", Value: tok, Path: "/", HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
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

// report returns the setup checks. Results are cached for a few seconds so
// several open browser tabs don't each run Docker commands.
func (p *Panel) report(ctx context.Context, fresh bool) Report {
	p.repMu.Lock()
	defer p.repMu.Unlock()
	if !fresh && time.Since(p.repAt) < 8*time.Second && len(p.rep.Checks) > 0 {
		return p.rep
	}
	p.rep = runChecks(ctx, p.cfg)
	p.repAt = time.Now()
	return p.rep
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
	if p.creds.Mode() == "setup" {
		writeJSON(w, 409, map[string]string{"error": "setup_required"})
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
	if !p.creds.Check(body.Password) {
		p.mu.Lock()
		p.fails = append(p.fails, time.Now())
		p.mu.Unlock()
		time.Sleep(time.Second)
		writeJSON(w, 401, map[string]string{"error": "Wrong password"})
		return
	}
	p.startSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setup creates the very first password. Only possible while none exists, and
// only from the local network.
func (p *Panel) setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	if p.creds.Mode() != "setup" {
		writeJSON(w, 409, map[string]string{"error": "A password is already set. Sign in instead."})
		return
	}
	if !privateClient(r) {
		writeJSON(w, 403, map[string]string{"error": "For safety, the first password can only be created from a device on your home network, not through the internet or a proxy."})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
	if err := ValidatePassword(body.Password); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := p.creds.SetFirst(body.Password); err != nil {
		if errors.Is(err, errAlreadySetUp) {
			writeJSON(w, 409, map[string]string{"error": "A password is already set. Sign in instead."})
		} else {
			writeJSON(w, 500, map[string]string{"error": "Could not save the password. Is the data folder writable?"})
		}
		return
	}
	p.startSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// changePassword replaces the saved password (needs the current one).
func (p *Panel) changePassword(w http.ResponseWriter, r *http.Request) {
	if p.creds.Mode() == "env" {
		writeJSON(w, 400, map[string]string{"error": "Your password is set by the PANEL_PASSWORD setting. Change it there and recreate the container."})
		return
	}
	if p.locked() {
		writeJSON(w, 429, map[string]string{"error": "too many wrong passwords, wait a minute"})
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
	if !p.creds.Check(body.Current) {
		p.mu.Lock()
		p.fails = append(p.fails, time.Now())
		p.mu.Unlock()
		time.Sleep(time.Second)
		writeJSON(w, 400, map[string]string{"error": "Your current password is wrong."})
		return
	}
	if err := ValidatePassword(body.New); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := p.creds.Change(body.New); err != nil {
		writeJSON(w, 500, map[string]string{"error": "Could not save the password. Is the data folder writable?"})
		return
	}
	p.startSession(w, r) // every other sign-in is now invalid; keep this browser signed in
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
		mode := p.creds.Mode()
		writeJSON(w, 200, map[string]any{"authenticated": p.valid(r), "mode": mode, "can_setup": mode == "setup" && privateClient(r)})
	})
	mux.HandleFunc("/api/login", p.login)
	mux.HandleFunc("/api/setup", p.setup)
	mux.HandleFunc("/api/password", p.auth(true, p.changePassword))
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
	mux.HandleFunc("/api/doctor", p.auth(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, p.report(r.Context(), r.URL.Query().Get("fresh") == "1"))
	}))
	mux.HandleFunc("/api/config", p.auth(true, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NCContainer string `json:"nc_container"`
			NCURL       string `json:"nc_url"`
			DBContainer string `json:"db_container"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "could not read the form"})
			return
		}
		nc, ncURL, db, err := validateConfigInput(body.NCContainer, body.NCURL, body.DBContainer)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if p.cfg.Demo {
			writeJSON(w, 400, map[string]string{"error": "Settings can't be changed in demo mode"})
			return
		}
		// stop the tunnel (if on) so the old trusted domain is cleaned up, save, start again
		p.m.Restart(func() {
			updateSettings(p.cfg, func(s *Settings) { s.NCContainer, s.NCURL, s.DBContainer = nc, ncURL, db })
		})
		writeJSON(w, 200, p.report(r.Context(), true))
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
		if p.cfg.Managed {
			writeJSON(w, 400, map[string]string{"error": "Updates are managed by CasaOS"})
			return
		}
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
