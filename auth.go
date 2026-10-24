package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The panel password lives in one of two places:
//
//	PANEL_PASSWORD (environment)  -> "env" mode. It always wins.
//	DATA_DIR/auth.json            -> "file" mode. Chosen by the user on first run.
//
// With neither, the panel is in "setup" mode: it asks for a new password, and
// only answers that request from the local network.
//
// To reset a forgotten password, delete auth.json (or run `sharetunnel
// reset-password`). Settings and shared links are not touched.

const (
	minPasswordLen      = 8
	maxPasswordLen      = 128
	pbkdf2Iterations    = 600000
	placeholderPassword = "change-me" // the old example value; counts as "not set"
)

var errAlreadySetUp = errors.New("a password is already set")

// pbkdf2SHA256 is PBKDF2-HMAC-SHA256 (RFC 8018). Written out here so the project
// needs no extra dependency; it is checked against the RFC test vectors.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (keyLen + hLen - 1) / hLen
	var counter [4]byte
	dk := make([]byte, 0, blocks*hLen)
	u := make([]byte, hLen)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		dk = prf.Sum(dk)
		t := dk[len(dk)-hLen:]
		copy(u, t)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range u {
				t[i] ^= u[i]
			}
		}
	}
	return dk[:keyLen]
}

type credFile struct {
	V    int    `json:"v"`
	Iter int    `json:"iter"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

type Auth struct {
	dir string
	env string // PANEL_PASSWORD, "" when not set
	mu  sync.Mutex
}

func NewAuth(cfg Config, envPassword string) *Auth {
	if envPassword == placeholderPassword {
		envPassword = ""
	}
	return &Auth{dir: cfg.DataDir, env: envPassword}
}

func (a *Auth) path() string { return filepath.Join(a.dir, "auth.json") }

// load reads auth.json fresh each time, so deleting or replacing the file
// (by hand or with reset-password) takes effect immediately.
func (a *Auth) load() (credFile, bool) {
	b, err := os.ReadFile(a.path())
	if err != nil {
		return credFile{}, false
	}
	var c credFile
	if json.Unmarshal(b, &c) != nil || c.Hash == "" || c.Salt == "" || c.Iter < 1 {
		return credFile{}, false
	}
	return c, true
}

// Mode is "env", "file" or "setup".
func (a *Auth) Mode() string {
	if a.env != "" {
		return "env"
	}
	if _, ok := a.load(); ok {
		return "file"
	}
	return "setup"
}

// Check reports whether pw is the current password.
func (a *Auth) Check(pw string) bool {
	if len(pw) > maxPasswordLen*4 {
		return false
	}
	if a.env != "" {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(a.env)) == 1
	}
	c, ok := a.load()
	if !ok {
		return false
	}
	salt, err1 := hex.DecodeString(c.Salt)
	want, err2 := hex.DecodeString(c.Hash)
	if err1 != nil || err2 != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(pw), salt, c.Iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Fingerprint changes whenever the password changes. Sessions remember the value
// they were created under, so changing or resetting the password signs everyone out.
// It is "" in setup mode (nobody can be signed in).
func (a *Auth) Fingerprint() string {
	if a.env != "" {
		sum := sha256.Sum256([]byte("env:" + a.env))
		return hex.EncodeToString(sum[:8])
	}
	if c, ok := a.load(); ok {
		return c.Hash[:min(16, len(c.Hash))]
	}
	return ""
}

// ValidatePassword is the rule for a new password.
func ValidatePassword(pw string) error {
	switch {
	case len(pw) < minPasswordLen:
		return fmt.Errorf("Use at least %d characters.", minPasswordLen)
	case len(pw) > maxPasswordLen:
		return fmt.Errorf("Use at most %d characters.", maxPasswordLen)
	case strings.EqualFold(pw, placeholderPassword):
		return errors.New("Pick a password of your own.")
	}
	return nil
}

func (a *Auth) write(pw string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	c := credFile{V: 1, Iter: pbkdf2Iterations, Salt: hex.EncodeToString(salt)}
	c.Hash = hex.EncodeToString(pbkdf2SHA256([]byte(pw), salt, c.Iter, 32))
	b, _ := json.Marshal(c)
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return err
	}
	tmp := a.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path())
}

// SetFirst creates the password, but only while none exists.
func (a *Auth) SetFirst(pw string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Mode() != "setup" {
		return errAlreadySetUp
	}
	return a.write(pw)
}

// Change replaces the saved password (file mode only; the caller checks the old one).
func (a *Auth) Change(pw string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.write(pw)
}

// ---------- who may create the first password ----------

var cgnat = func() *net.IPNet { _, n, _ := net.ParseCIDR("100.64.0.0/10"); return n }() // Tailscale and similar

// privateClient is true when the request comes straight from this machine or a
// private network. Requests that pass through a proxy are refused, because their
// real address can't be trusted.
func privateClient(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// ---------- sharetunnel reset-password ----------

func randomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no look-alike characters
	out := make([]byte, 16)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// resetPassword is the `sharetunnel reset-password` command. It writes a new
// random password and prints it; every old sign-in stops working.
func resetPassword(cfg Config) {
	if pw := os.Getenv("PANEL_PASSWORD"); pw != "" && pw != placeholderPassword {
		fmt.Println("Your password is set by the PANEL_PASSWORD setting, so there is nothing to reset here.")
		fmt.Println("Change PANEL_PASSWORD and recreate the container instead.")
		os.Exit(1)
	}
	pw := randomPassword()
	if err := NewAuth(cfg, "").Change(pw); err != nil {
		fmt.Println("Could not save the new password:", err)
		fmt.Println("Check that the data folder (" + cfg.DataDir + ") is writable.")
		os.Exit(1)
	}
	fmt.Println("Your new ShareTunnel password is:")
	fmt.Println()
	fmt.Println("    " + pw)
	fmt.Println()
	fmt.Println("Sign in with it, then change it on the Setup page. Everyone who was signed in is signed out.")
}
