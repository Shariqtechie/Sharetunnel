// ShareTunnel - milestone 1: core engine (command line)
//
//	sharetunnel run      start tunnel, trust its URL in Nextcloud, list shares
//	sharetunnel shares   just list public share links
//	sharetunnel version
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

var urlRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// ---------- config (from environment variables) ----------

type Config struct {
	NCContainer  string // Nextcloud container name
	NCURL        string // where cloudflared reaches Nextcloud
	DBContainer  string // Postgres container name
	TrustedIndex string // the trusted_domains slot this app owns
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	return Config{
		NCContainer:  getenv("NC_CONTAINER", "nextcloud"),
		NCURL:        getenv("NC_URL", "http://localhost:7580"),
		DBContainer:  getenv("DB_CONTAINER", "db-nextcloud"),
		TrustedIndex: getenv("TRUSTED_INDEX", "50"),
	}
}

// ---------- Nextcloud trusted_domains ----------

func occ(cfg Config, args ...string) (string, error) {
	full := append([]string{"exec", "-u", "www-data", cfg.NCContainer, "php", "occ"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// We always use ONE fixed slot, so the old tunnel URL is replaced
// instead of piling up in config.php.
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
}

const sharesSQL = `SELECT COALESCE(json_agg(t ORDER BY t.id DESC), '[]') FROM (
  SELECT s.id, s.token, s.item_type, COALESCE(m.mimetype,'') AS mimetype,
         COALESCE(f.size,0) AS size, s.file_target, s.uid_owner AS owner,
         s.expiration, (s.password IS NOT NULL) AS has_pw
  FROM oc_share s
  LEFT JOIN oc_filecache f ON f.fileid = s.file_source
  LEFT JOIN oc_mimetypes m ON m.id = f.mimetype
  WHERE s.share_type = 3
) t`

func listShares(cfg Config) ([]Share, error) {
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
	return shares, nil
}

func kind(s Share) (icon, label string) {
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
	if len(shares) == 0 {
		fmt.Println("No public share links yet.")
		return
	}
	fmt.Printf("\n%d public share link(s):\n", len(shares))
	for _, s := range shares {
		icon, label := kind(s)
		extra := ""
		if s.HasPW {
			extra += " 🔒 password"
		}
		if s.Expiration != nil {
			extra += " ⏳ expires " + *s.Expiration
		}
		fmt.Printf("%s %s  [%s, %s, owner: %s]%s\n    %s/s/%s\n",
			icon, strings.TrimPrefix(s.Name, "/"), label, humanSize(s.Size), s.Owner, extra, base, s.Token)
	}
	fmt.Println()
}

// ---------- the tunnel ----------

// runTunnel starts cloudflared and calls onURL once when the public URL appears.
// It returns when cloudflared exits.
func runTunnel(ctx context.Context, cfg Config, onURL func(string)) error {
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
		if strings.Contains(line, " ERR ") || strings.Contains(line, " WRN ") {
			fmt.Println("[cloudflared]", line)
		}
	}
	return cmd.Wait()
}

func run(cfg Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("ShareTunnel %s | Nextcloud container: %s | target: %s\n", version, cfg.NCContainer, cfg.NCURL)

	onURL := func(u string) {
		host := strings.TrimPrefix(u, "https://")
		fmt.Println("Tunnel is up:", u)
		if err := setTrusted(cfg, host); err != nil {
			fmt.Println("ERROR: could not add trusted domain:", err)
			return
		}
		fmt.Println("Nextcloud now trusts", host)
		printShares(cfg, u)
	}

	for ctx.Err() == nil {
		err := runTunnel(ctx, cfg, onURL)
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("Tunnel stopped (%v). Restarting in 5s...\n", err)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}

	fmt.Println("Stopping: removing tunnel URL from trusted_domains")
	clearTrusted(cfg)
}

func main() {
	cfg := loadConfig()
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "run":
		run(cfg)
	case "shares":
		printShares(cfg, "")
	case "version":
		fmt.Println(version)
	default:
		fmt.Println("usage: sharetunnel [run|shares|version]")
		os.Exit(1)
	}
}
