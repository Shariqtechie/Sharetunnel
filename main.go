// ShareTunnel - share Nextcloud files with anyone through a temporary Cloudflare link.
//
//	sharetunnel serve    web panel + tunnel manager (default in Docker)
//	sharetunnel run      command-line mode (no panel)
//	sharetunnel shares   list public share links
//	sharetunnel version
//	sharetunnel apply-update   (internal: used by the updater helper container)
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
)

var version = "dev"

var urlRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// ---------- config (environment variables) ----------

type Config struct {
	NCContainer   string // Nextcloud container name
	NCURL         string // where cloudflared reaches Nextcloud
	DBContainer   string // Postgres container name
	TrustedIndex  string // the trusted_domains slot this app owns
	PanelPort     string
	DataDir       string
	Repo          string // GitHub owner/name for update checks
	SelfContainer string // this container's own name
	Demo          bool   // DEMO=true: fake data, no Docker or tunnel needed
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	return Config{
		NCContainer:   getenv("NC_CONTAINER", "nextcloud"),
		NCURL:         getenv("NC_URL", "http://localhost:7580"),
		DBContainer:   getenv("DB_CONTAINER", "db-nextcloud"),
		TrustedIndex:  getenv("TRUSTED_INDEX", "50"),
		PanelPort:     getenv("PANEL_PORT", "9099"),
		DataDir:       getenv("DATA_DIR", "/data"),
		Repo:          getenv("GITHUB_REPO", "Shariqtechie/Sharetunnel"),
		SelfContainer: getenv("SELF_CONTAINER", "sharetunnel"),
		Demo:          os.Getenv("DEMO") == "true",
	}
}

// ---------- Nextcloud trusted_domains ----------

func occ(cfg Config, args ...string) (string, error) {
	full := append([]string{"exec", "-u", "www-data", cfg.NCContainer, "php", "occ"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// One fixed slot, so the old tunnel URL is replaced instead of piling up.
func setTrusted(cfg Config, host string) error {
	out, err := occ(cfg, "config:system:set", "trusted_domains", cfg.TrustedIndex, "--value="+host)
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func clearTrusted(cfg Config) {
	occ(cfg, "config:system:delete", "trusted_domains", cfg.TrustedIndex)
}

// ---------- shares (read-only database query) ----------

type Share struct {
	ID         int     `json:"id"`
	Token      string  `json:"token"`
	ItemType   string  `json:"item_type"`
	Mimetype   string  `json:"mimetype"`
	Size       int64   `json:"size"`
	Name       string  `json:"file_target"`
	Owner      string  `json:"owner"`
	Expiration *string `json:"expiration"`
	HasPW      bool    `json:"has_pw"`
	Created    int64   `json:"created"` // unix seconds
}

type ShareOut struct {
	Share
	Kind     string `json:"kind"`
	Icon     string `json:"icon"`
	SizeText string `json:"size_text"`
}

const sharesSQL = `SELECT COALESCE(json_agg(t ORDER BY t.id DESC), '[]') FROM (
  SELECT s.id, s.token, s.item_type, COALESCE(m.mimetype,'') AS mimetype,
         COALESCE(f.size,0) AS size, s.file_target, s.uid_owner AS owner,
         s.expiration, (s.password IS NOT NULL) AS has_pw, s.stime AS created
  FROM oc_share s
  LEFT JOIN oc_filecache f ON f.fileid = s.file_source
  LEFT JOIN oc_mimetypes m ON m.id = f.mimetype
  WHERE s.share_type = 3
) t`

func listShares(cfg Config) ([]ShareOut, error) {
	if cfg.Demo {
		return demoShares(), nil
	}
	cmd := exec.Command("docker", "exec",
		"-e", "SQL="+sharesSQL,
		"-e", "PGOPTIONS=-c default_transaction_read_only=on", // read-only, always
		cfg.DBContainer, "sh", "-c",
		`psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$SQL"`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var shares []Share
	if err := json.Unmarshal(bytes.TrimSpace(out), &shares); err != nil {
		return nil, fmt.Errorf("bad JSON from database: %v", err)
	}
	res := make([]ShareOut, 0, len(shares))
	for _, s := range shares {
		icon, kind := kindOf(s)
		res = append(res, ShareOut{Share: s, Kind: kind, Icon: icon, SizeText: humanSize(s.Size)})
	}
	return res, nil
}

func kindOf(s Share) (icon, label string) {
	switch {
	case s.ItemType == "folder":
		return "📁", "folder"
	case strings.HasPrefix(s.Mimetype, "video/"):
		return "🎬", "video"
	case strings.HasPrefix(s.Mimetype, "image/"):
		return "🖼️", "image"
	case strings.HasPrefix(s.Mimetype, "audio/"):
		return "🎵", "audio"
	}
	return "📄", "file"
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func printShares(cfg Config, base string) {
	shares, err := listShares(cfg)
	if err != nil {
		fmt.Println("Could not read shares:", err)
		return
	}
	for _, s := range shares {
		fmt.Printf("%s %s [%s, %s, owner: %s]\n    %s/s/%s\n", s.Icon, strings.TrimPrefix(s.Name, "/"), s.Kind, s.SizeText, s.Owner, base, s.Token)
	}
}

// ---------- the tunnel process ----------

// runTunnel starts cloudflared, calls onURL once when the public URL appears,
// and returns when cloudflared exits.
func runTunnel(ctx context.Context, cfg Config, logf func(string, ...any), onURL func(string)) error {
	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "--no-autoupdate", "--url", cfg.NCURL)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	found := false
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if !found {
			if u := urlRe.FindString(line); u != "" && !strings.HasPrefix(u, "https://api.") {
				found = true
				go onURL(u)
			}
		}
		if strings.Contains(line, "canceled by remote") {
			continue // someone stopped a download, harmless
		}
		if strings.Contains(line, " ERR ") || strings.Contains(line, " WRN ") {
			logf("[cloudflared] %s", line)
		}
	}
	return cmd.Wait()
}

// ---------- commands ----------

func serve(cfg Config) {
	pw := os.Getenv("PANEL_PASSWORD")
	if cfg.Demo && pw == "" {
		pw = "demo"
	}
	if pw == "" || pw == "change-me" {
		fmt.Println("ERROR: set PANEL_PASSWORD to a real password (not empty, not 'change-me').")
		os.Exit(1)
	}
	m := NewManager(cfg, true)
	if loadSettings(cfg).Enabled {
		m.Start()
	}
	u := NewUpdater(cfg, m)
	srv := &http.Server{Addr: ":" + cfg.PanelPort, Handler: NewPanel(cfg, m, u, pw).routes()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go u.loop(ctx)
	go reportLastUpdate(ctx, m)
	go func() {
		<-ctx.Done()
		m.Shutdown() // removes the trusted domain, keeps the saved on/off choice
		srv.Shutdown(context.Background())
	}()

	m.logf("ShareTunnel %s - panel on port %s", version, cfg.PanelPort)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
}

func run(cfg Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m := NewManager(cfg, false)
	m.Start()
	<-ctx.Done()
	m.Shutdown()
}

func main() {
	cfg := loadConfig()
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		serve(cfg)
	case "run":
		run(cfg)
	case "shares":
		printShares(cfg, "")
	case "apply-update":
		applyUpdate()
	case "version":
		fmt.Println(version)
	default:
		fmt.Println("usage: sharetunnel [serve|run|shares|version]")
		os.Exit(1)
	}
}
