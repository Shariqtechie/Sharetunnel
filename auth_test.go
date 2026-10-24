package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test vectors for PBKDF2-HMAC-SHA256 (from RFC 7914 / widely published).
func TestPBKDF2Vectors(t *testing.T) {
	cases := []struct {
		pw, salt string
		iter     int
		want     string
	}{
		{"password", "salt", 1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(pbkdf2SHA256([]byte(c.pw), []byte(c.salt), c.iter, 32)); got != c.want {
			t.Errorf("pbkdf2(%q,%q,%d) = %s, want %s", c.pw, c.salt, c.iter, got, c.want)
		}
	}
}

func TestPrivateClient(t *testing.T) {
	cases := []struct {
		addr    string
		headers map[string]string
		want    bool
	}{
		{"192.168.0.151:5000", nil, true},
		{"10.1.2.3:1", nil, true},
		{"172.20.0.5:1", nil, true},
		{"127.0.0.1:1", nil, true},
		{"[::1]:1", nil, true},
		{"100.101.102.103:1", nil, true}, // Tailscale
		{"[fd12:3456::1]:1", nil, true},
		{"8.8.8.8:1", nil, false},
		{"172.32.0.1:1", nil, false}, // just outside 172.16/12
		{"[2606:4700::1]:1", nil, false},
		{"192.168.0.5:1", map[string]string{"X-Forwarded-For": "8.8.8.8"}, false}, // came through a proxy
		{"127.0.0.1:1", map[string]string{"X-Real-Ip": "8.8.8.8"}, false},
		{"garbage", nil, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.addr
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := privateClient(r); got != c.want {
			t.Errorf("privateClient(%s %v) = %v, want %v", c.addr, c.headers, got, c.want)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for _, bad := range []string{"", "short", "1234567", "change-me", "CHANGE-ME", strings.Repeat("x", 129)} {
		if ValidatePassword(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	for _, ok := range []string{"12345678", "a good passphrase!", strings.Repeat("x", 128)} {
		if err := ValidatePassword(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
}

// ---- panel flow helpers ----

type testPanel struct {
	t   *testing.T
	h   http.Handler
	dir string
}

func newTestPanel(t *testing.T, envPassword string, managed bool) *testPanel {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	cfg := loadConfig()
	cfg.Managed = managed
	m := NewManager(cfg, false)
	return &testPanel{t: t, dir: dir, h: NewPanel(cfg, m, NewUpdater(cfg, m), envPassword).routes()}
}

type resp struct {
	code    int
	body    map[string]any
	cookies []*http.Cookie
}

func (p *testPanel) do(method, path string, body any, remote string, cookie *http.Cookie, hdr map[string]string) resp {
	p.t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = remote
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	out := resp{code: w.Code, cookies: w.Result().Cookies()}
	json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

const lan = "192.168.0.50:4000"

func sessionCookie(r resp) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == "st_session" {
			return c
		}
	}
	return nil
}

func TestFirstRunSetupFlow(t *testing.T) {
	p := newTestPanel(t, "", false)

	// fresh install: setup mode, offered only to the local network
	r := p.do("GET", "/api/session", nil, lan, nil, nil)
	if r.body["mode"] != "setup" || r.body["can_setup"] != true || r.body["authenticated"] != false {
		t.Fatalf("fresh install session = %v", r.body)
	}
	if r := p.do("GET", "/api/session", nil, "8.8.8.8:1", nil, nil); r.body["can_setup"] != false {
		t.Error("the internet must not be offered setup")
	}
	if r := p.do("POST", "/api/login", map[string]string{"password": "whatever1"}, lan, nil, nil); r.code != 409 {
		t.Errorf("login before setup should say setup_required, got %d", r.code)
	}
	if r := p.do("GET", "/api/status", nil, lan, nil, nil); r.code != 401 {
		t.Error("API must be locked before setup")
	}

	// refused: from the internet, through a proxy, too short, placeholder
	if r := p.do("POST", "/api/setup", map[string]string{"password": "a-long-password"}, "8.8.8.8:1", nil, nil); r.code != 403 {
		t.Errorf("setup from the internet got %d", r.code)
	}
	if r := p.do("POST", "/api/setup", map[string]string{"password": "a-long-password"}, "127.0.0.1:1", nil, map[string]string{"X-Forwarded-For": "8.8.8.8"}); r.code != 403 {
		t.Errorf("setup through a proxy got %d", r.code)
	}
	for _, bad := range []string{"short", "change-me"} {
		if r := p.do("POST", "/api/setup", map[string]string{"password": bad}, lan, nil, nil); r.code != 400 {
			t.Errorf("setup with %q got %d", bad, r.code)
		}
	}
	if _, err := os.Stat(filepath.Join(p.dir, "auth.json")); err == nil {
		t.Fatal("nothing should be saved by refused attempts")
	}

	// success
	r = p.do("POST", "/api/setup", map[string]string{"password": "my-new-password"}, lan, nil, nil)
	ck := sessionCookie(r)
	if r.code != 200 || ck == nil {
		t.Fatalf("setup failed: %d %v", r.code, r.body)
	}
	if r := p.do("GET", "/api/status", nil, lan, ck, nil); r.code != 200 {
		t.Errorf("signed-in request got %d", r.code)
	}
	raw, _ := os.ReadFile(filepath.Join(p.dir, "auth.json"))
	if strings.Contains(string(raw), "my-new-password") {
		t.Error("the password must not be stored in plain text")
	}
	if fi, _ := os.Stat(filepath.Join(p.dir, "auth.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("auth.json permissions = %v, want 0600", fi.Mode().Perm())
	}

	// setup can only happen once
	if r := p.do("POST", "/api/setup", map[string]string{"password": "another-password"}, lan, nil, nil); r.code != 409 {
		t.Errorf("second setup got %d", r.code)
	}

	// normal sign-in afterwards
	if r := p.do("POST", "/api/login", map[string]string{"password": "wrong-password"}, lan, nil, nil); r.code != 401 {
		t.Errorf("wrong password got %d", r.code)
	}
	if r := p.do("POST", "/api/login", map[string]string{"password": "my-new-password"}, lan, nil, nil); r.code != 200 || sessionCookie(r) == nil {
		t.Errorf("right password got %d", r.code)
	}
}

func TestChangePasswordSignsOutOtherBrowsers(t *testing.T) {
	p := newTestPanel(t, "", false)
	a := sessionCookie(p.do("POST", "/api/setup", map[string]string{"password": "first-password"}, lan, nil, nil))
	b := sessionCookie(p.do("POST", "/api/login", map[string]string{"password": "first-password"}, lan, nil, nil))

	if r := p.do("POST", "/api/password", map[string]string{"current": "nope-nope", "new": "second-password"}, lan, a, nil); r.code != 400 {
		t.Errorf("wrong current password got %d", r.code)
	}
	if r := p.do("POST", "/api/password", map[string]string{"current": "first-password", "new": "tiny"}, lan, a, nil); r.code != 400 {
		t.Errorf("weak new password got %d", r.code)
	}
	r := p.do("POST", "/api/password", map[string]string{"current": "first-password", "new": "second-password"}, lan, a, nil)
	a2 := sessionCookie(r)
	if r.code != 200 || a2 == nil {
		t.Fatalf("change failed: %d %v", r.code, r.body)
	}
	if p.do("GET", "/api/status", nil, lan, a2, nil).code != 200 {
		t.Error("the browser that changed it should stay signed in")
	}
	if p.do("GET", "/api/status", nil, lan, b, nil).code != 401 || p.do("GET", "/api/status", nil, lan, a, nil).code != 401 {
		t.Error("old sign-ins must stop working after a password change")
	}
	if p.do("POST", "/api/login", map[string]string{"password": "first-password"}, lan, nil, nil).code != 401 {
		t.Error("the old password must stop working")
	}
	if p.do("POST", "/api/login", map[string]string{"password": "second-password"}, lan, nil, nil).code != 200 {
		t.Error("the new password must work")
	}
}

// Forgot-password flow 1: delete auth.json.
func TestDeletingAuthFileResetsToSetup(t *testing.T) {
	p := newTestPanel(t, "", false)
	ck := sessionCookie(p.do("POST", "/api/setup", map[string]string{"password": "first-password"}, lan, nil, nil))
	if err := os.Remove(filepath.Join(p.dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if p.do("GET", "/api/status", nil, lan, ck, nil).code != 401 {
		t.Error("old sign-in must stop working once auth.json is gone")
	}
	if r := p.do("GET", "/api/session", nil, lan, nil, nil); r.body["mode"] != "setup" || r.body["can_setup"] != true {
		t.Errorf("session after delete = %v", r.body)
	}
	if p.do("POST", "/api/setup", map[string]string{"password": "brand-new-password"}, lan, nil, nil).code != 200 {
		t.Error("creating a new password after delete failed")
	}
}

// Forgot-password flow 2: `sharetunnel reset-password` writes the file from another process.
func TestResetPasswordCommand(t *testing.T) {
	p := newTestPanel(t, "", false)
	ck := sessionCookie(p.do("POST", "/api/setup", map[string]string{"password": "first-password"}, lan, nil, nil))
	newPw := randomPassword()
	if len(newPw) != 16 {
		t.Fatalf("generated password %q", newPw)
	}
	if err := NewAuth(loadConfig(), "").Change(newPw); err != nil { // what resetPassword does
		t.Fatal(err)
	}
	if p.do("GET", "/api/status", nil, lan, ck, nil).code != 401 {
		t.Error("everyone must be signed out after a reset")
	}
	if p.do("POST", "/api/login", map[string]string{"password": "first-password"}, lan, nil, nil).code != 401 {
		t.Error("the old password must not work after a reset")
	}
	if p.do("POST", "/api/login", map[string]string{"password": newPw}, lan, nil, nil).code != 200 {
		t.Error("the new random password must work")
	}
}

func TestEnvPasswordWins(t *testing.T) {
	p := newTestPanel(t, "from-the-env", false)
	if r := p.do("GET", "/api/session", nil, lan, nil, nil); r.body["mode"] != "env" || r.body["can_setup"] != false {
		t.Errorf("env mode session = %v", r.body)
	}
	ck := sessionCookie(p.do("POST", "/api/login", map[string]string{"password": "from-the-env"}, lan, nil, nil))
	if ck == nil {
		t.Fatal("env password should sign in")
	}
	if p.do("POST", "/api/setup", map[string]string{"password": "something-else"}, lan, nil, nil).code != 409 {
		t.Error("setup must be refused when PANEL_PASSWORD is set")
	}
	r := p.do("POST", "/api/password", map[string]string{"current": "from-the-env", "new": "something-else"}, lan, ck, nil)
	if r.code != 400 || !strings.Contains(r.body["error"].(string), "PANEL_PASSWORD") {
		t.Errorf("changing an env password should explain PANEL_PASSWORD, got %d %v", r.code, r.body)
	}
}

func TestPlaceholderPasswordCountsAsNotSet(t *testing.T) {
	p := newTestPanel(t, "change-me", false)
	if r := p.do("GET", "/api/session", nil, lan, nil, nil); r.body["mode"] != "setup" {
		t.Errorf("the old placeholder should start first-run setup, got %v", r.body)
	}
	if p.do("POST", "/api/login", map[string]string{"password": "change-me"}, lan, nil, nil).code == 200 {
		t.Error("nobody may sign in with the placeholder")
	}
}

func TestManagedUpdates(t *testing.T) {
	p := newTestPanel(t, "an-env-password", true)
	ck := sessionCookie(p.do("POST", "/api/login", map[string]string{"password": "an-env-password"}, lan, nil, nil))
	if r := p.do("GET", "/api/update", nil, lan, ck, nil); r.body["managed"] != true {
		t.Errorf("update info should say managed: %v", r.body)
	}
	if p.do("POST", "/api/update/settings", map[string]any{"mode": "auto", "hour": 3}, lan, ck, nil).code != 400 {
		t.Error("preferences must be locked when managed")
	}
	cfg := loadConfig()
	cfg.Managed = true
	u := NewUpdater(cfg, NewManager(cfg, false))
	u.info.Available = true
	if err := u.Apply(); err == nil || !strings.Contains(err.Error(), "CasaOS") {
		t.Errorf("Apply must refuse when managed, got %v", err)
	}
}

func TestFreshInstallTunnelStartsOff(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	if loadSettings(loadConfig()).Enabled {
		t.Error("a fresh install must not start the tunnel by itself")
	}
}
