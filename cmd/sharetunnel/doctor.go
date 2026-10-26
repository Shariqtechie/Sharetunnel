package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The "doctor" checks that ShareTunnel can do its job and, when it cannot,
// says what is wrong and what to do about it in plain words.

type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type ConfigView struct {
	NCContainer string `json:"nc_container"`
	NCURL       string `json:"nc_url"`
	DBContainer string `json:"db_container"`
	PanelPort   string `json:"panel_port"`
}

type Report struct {
	Checks     []Check    `json:"checks"`
	Ready      bool       `json:"ready"` // no check failed
	Nextcloud  []string   `json:"nextcloud_candidates"`
	Postgres   []string   `json:"postgres_candidates"`
	SuggestURL string     `json:"suggested_url"`
	Config     ConfigView `json:"config"`
}

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	portRe = regexp.MustCompile(`:(\d+)$`)
)

// dockerCmd runs the docker CLI with a time limit.
func dockerCmd(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return s, fmt.Errorf("timed out after %s", timeout)
	}
	return s, err
}

// friendly turns raw Docker / database errors into a short sentence a person can act on.
func friendly(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "executable file not found"):
		return "The Docker command-line tool is missing inside this container. Pull the official image again."
	case strings.Contains(l, "permission denied") && strings.Contains(l, "docker"):
		return "ShareTunnel is not allowed to use Docker. Mount the Docker socket (-v /var/run/docker.sock:/var/run/docker.sock) and run the container as root."
	case strings.Contains(l, "cannot connect to the docker daemon"),
		strings.Contains(l, "connect to the docker api"),
		strings.Contains(l, "docker.sock") && strings.Contains(l, "no such file"):
		return "Can't reach Docker. Add -v /var/run/docker.sock:/var/run/docker.sock to the container."
	case strings.Contains(l, "no such container"), strings.Contains(l, "no such object"):
		return "That container doesn't exist. Check its name on the Setup page."
	case strings.Contains(l, "is not running"):
		return "That container is stopped. Start it, then try again."
	case strings.Contains(l, "password authentication failed"):
		return "The database refused the login. This is not the usual Nextcloud PostgreSQL setup."
	case strings.Contains(l, "does not exist") && (strings.Contains(l, "role") || strings.Contains(l, "database") || strings.Contains(l, "relation")):
		return "This doesn't look like Nextcloud's PostgreSQL database. Check the database container on the Setup page."
	case strings.Contains(l, "timed out"):
		return "Docker did not answer in time. The server may be busy; try again in a moment."
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(msg), "\n", 2)[0])
	if r := []rune(first); len(r) > 200 {
		first = string(r[:200]) + "…"
	}
	if first == "" {
		first = "Unknown error"
	}
	return first
}

// detect lists running containers that look like Nextcloud or PostgreSQL.
func detect(ctx context.Context) (nc, pg []string) {
	out, err := dockerCmd(ctx, 6*time.Second, "ps", "--format", "{{.Names}}|{{.Image}}")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) != 2 {
			continue
		}
		name, img := parts[0], strings.ToLower(parts[1])
		switch {
		case strings.Contains(img, "nextcloud") && !strings.Contains(strings.ToLower(name), "cron"):
			nc = append(nc, name)
		case strings.Contains(img, "postgres"):
			pg = append(pg, name)
		}
	}
	// databases with "nextcloud" in the name are the likeliest match
	sort.SliceStable(pg, func(i, j int) bool {
		return strings.Contains(pg[i], "nextcloud") && !strings.Contains(pg[j], "nextcloud")
	})
	return
}

// suggestURL reads the published port of a Nextcloud container (80/tcp).
func suggestURL(ctx context.Context, name string) string {
	out, err := dockerCmd(ctx, 5*time.Second, "port", name, "80/tcp")
	if err != nil {
		return ""
	}
	first := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if m := portRe.FindStringSubmatch(first); m != nil {
		return "http://localhost:" + m[1]
	}
	return ""
}

func viewOf(cfg Config) ConfigView {
	return ConfigView{NCContainer: cfg.NCContainer, NCURL: cfg.NCURL, DBContainer: cfg.DBContainer, PanelPort: cfg.PanelPort}
}

func demoReport(cfg Config) Report {
	ok := func(id, title, detail string) Check { return Check{ID: id, Title: title, Status: "ok", Detail: detail} }
	return Report{
		Ready: true,
		Checks: []Check{
			ok("docker", "Docker access", "Connected to Docker (demo)."),
			ok("nextcloud", "Nextcloud container", `Found "nextcloud" running.`),
			ok("occ", "Nextcloud is installed", "Nextcloud answered the setup check."),
			ok("url", "Nextcloud address", "Nextcloud answered at http://localhost:7580."),
			ok("database", "Share database", "Connected, Nextcloud tables found."),
			ok("cloudflared", "Cloudflare tunnel tool", "cloudflared is installed."),
		},
		Nextcloud:  []string{"nextcloud"},
		Postgres:   []string{"db-nextcloud"},
		SuggestURL: "http://localhost:7580",
		Config:     viewOf(cfg),
	}
}

// runChecks looks at everything ShareTunnel depends on. It never panics and never
// blocks for long: every external call has a time limit.
func runChecks(ctx context.Context, cfg Config) Report {
	cfg = effectiveConfig(cfg)
	if cfg.Demo {
		return demoReport(cfg)
	}
	rep := Report{Config: viewOf(cfg)}
	add := func(id, title, status, detail, fix string) {
		rep.Checks = append(rep.Checks, Check{ID: id, Title: title, Status: status, Detail: detail, Fix: fix})
	}
	cloudflaredCheck := func() {
		if _, err := exec.LookPath("cloudflared"); err != nil {
			add("cloudflared", "Cloudflare tunnel tool", "fail", "cloudflared is missing from this container.", "Pull the official image again: docker pull ghcr.io/shariqtechie/sharetunnel:latest")
		} else {
			add("cloudflared", "Cloudflare tunnel tool", "ok", "cloudflared is installed.", "")
		}
	}
	finish := func() Report {
		rep.Ready = true
		for _, c := range rep.Checks {
			if c.Status == "fail" {
				rep.Ready = false
			}
		}
		return rep
	}

	// 1. Docker
	if out, err := dockerCmd(ctx, 6*time.Second, "version", "--format", "{{.Server.Version}}"); err != nil {
		raw := out
		if raw == "" {
			raw = err.Error()
		}
		add("docker", "Docker access", "fail", friendly(raw), "Start the container with: -v /var/run/docker.sock:/var/run/docker.sock")
		cloudflaredCheck()
		return finish()
	} else {
		add("docker", "Docker access", "ok", "Connected to Docker "+out+".", "")
	}

	rep.Nextcloud, rep.Postgres = detect(ctx)
	rep.SuggestURL = ""

	// 2. Nextcloud container
	ncOK := false
	state, err := dockerCmd(ctx, 5*time.Second, "inspect", "-f", "{{.State.Running}}", cfg.NCContainer)
	switch {
	case err != nil && strings.Contains(strings.ToLower(state), "no such"):
		fix := `Enter your Nextcloud container's name on the Setup page. To see all containers, run: sudo docker ps`
		if len(rep.Nextcloud) > 0 {
			fix = "Found: " + strings.Join(rep.Nextcloud, ", ") + ". Pick the right one on the Setup page."
		}
		if len(rep.Nextcloud) > 0 {
			rep.SuggestURL = suggestURL(ctx, rep.Nextcloud[0])
		}
		add("nextcloud", "Nextcloud container", "fail", fmt.Sprintf("No container named %q was found.", cfg.NCContainer), fix)
	case err != nil:
		add("nextcloud", "Nextcloud container", "fail", friendly(state), "")
	case state != "true":
		add("nextcloud", "Nextcloud container", "fail", fmt.Sprintf("%q exists but is stopped.", cfg.NCContainer), "Start Nextcloud, then press Re-check.")
	default:
		ncOK = true
		add("nextcloud", "Nextcloud container", "ok", fmt.Sprintf("Found %q running.", cfg.NCContainer), "")
	}

	if ncOK {
		rep.SuggestURL = suggestURL(ctx, cfg.NCContainer)

		// 3. occ works and Nextcloud is installed
		out, err := dockerCmd(ctx, 25*time.Second, "exec", "-u", "www-data", cfg.NCContainer, "php", "occ", "status")
		switch {
		case err != nil:
			add("occ", "Nextcloud is installed", "fail", friendly(out), "Make sure this is the Nextcloud app container (the one that runs PHP), not the database or a proxy.")
		case !strings.Contains(out, "installed: true"):
			add("occ", "Nextcloud is installed", "fail", "Nextcloud is not fully installed yet.", "Open Nextcloud in your browser and finish its setup first.")
		default:
			add("occ", "Nextcloud is installed", "ok", "Nextcloud answered the setup check.", "")
		}

		// 4. Nextcloud address (what the tunnel will point at)
		addURL := func(status, detail, fix string) { add("url", "Nextcloud address", status, detail, fix) }
		u, perr := url.Parse(cfg.NCURL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			addURL("fail", fmt.Sprintf("%q is not a valid address.", cfg.NCURL), "Use a form like http://localhost:7580")
		} else {
			client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.NCURL, "/")+"/status.php", nil)
			resp, err := client.Do(req)
			if err != nil {
				fix := "Find Nextcloud's port with: sudo docker port " + cfg.NCContainer + "  then enter it on the Setup page, for example http://localhost:8080"
				if rep.SuggestURL != "" && rep.SuggestURL != cfg.NCURL {
					fix = "Nextcloud looks like it is on " + rep.SuggestURL + ". Use that address on the Setup page."
				}
				addURL("fail", fmt.Sprintf("Nothing is answering at %s.", cfg.NCURL), fix)
			} else {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				switch {
				case strings.Contains(string(body), `"installed"`):
					addURL("ok", "Nextcloud answered at "+cfg.NCURL+".", "")
				case resp.StatusCode >= 300 && resp.StatusCode < 400:
					addURL("warn", fmt.Sprintf("%s redirects to %s.", cfg.NCURL, resp.Header.Get("Location")), "If sharing doesn't work, use the final address instead (for example https://...).")
				default:
					addURL("warn", fmt.Sprintf("Something answered at %s (HTTP %d), but it doesn't look like Nextcloud.", cfg.NCURL, resp.StatusCode), "Check the address and port on the Setup page.")
				}
			}
		}
	}

	// 5. Share database (optional: the tunnel works without it)
	const dbNote = "The tunnel still works; only the Shared links list needs this. "
	info, err := dockerCmd(ctx, 5*time.Second, "inspect", "-f", "{{.Config.Image}}|{{.State.Running}}", cfg.DBContainer)
	switch {
	case err != nil && strings.Contains(strings.ToLower(info), "no such"):
		fix := "Enter your PostgreSQL container's name on the Setup page."
		if len(rep.Postgres) > 0 {
			fix = "Found: " + strings.Join(rep.Postgres, ", ") + ". Pick the right one on the Setup page."
		}
		add("database", "Share database", "warn", dbNote+fmt.Sprintf("No container named %q was found.", cfg.DBContainer), fix)
	case err != nil:
		add("database", "Share database", "warn", dbNote+friendly(info), "")
	default:
		parts := strings.SplitN(info, "|", 2)
		img := strings.ToLower(parts[0])
		switch {
		case strings.Contains(img, "mysql") || strings.Contains(img, "mariadb"):
			add("database", "Share database", "warn", dbNote+"This is a MySQL/MariaDB database, which ShareTunnel doesn't support yet.", "Only PostgreSQL works for now.")
		case len(parts) < 2 || parts[1] != "true":
			add("database", "Share database", "warn", dbNote+fmt.Sprintf("%q is stopped.", cfg.DBContainer), "Start it, then press Re-check.")
		default:
			out, err := dockerCmd(ctx, 12*time.Second, "exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on", cfg.DBContainer, "sh", "-c",
				`psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "select count(*) from oc_share"`)
			if err != nil {
				add("database", "Share database", "warn", dbNote+friendly(out), "Check the database container name on the Setup page.")
			} else {
				add("database", "Share database", "ok", "Connected, Nextcloud tables found.", "")
			}
		}
	}

	// 6. cloudflared
	cloudflaredCheck()
	return finish()
}

// criticalProblem returns one plain sentence describing why the tunnel can't
// start, or "" when everything needed is fine.
func criticalProblem(ctx context.Context, cfg Config) string {
	for _, c := range runChecks(ctx, cfg).Checks {
		if c.Status == "fail" {
			if c.Fix != "" {
				return c.Title + ": " + c.Detail + " " + c.Fix
			}
			return c.Title + ": " + c.Detail
		}
	}
	return ""
}

// validateConfigInput checks values typed on the Setup page. Empty means "use the default".
func validateConfigInput(nc, ncURL, db string) (string, string, string, error) {
	nc, ncURL, db = strings.TrimSpace(nc), strings.TrimSpace(ncURL), strings.TrimSpace(db)
	for label, v := range map[string]string{"Nextcloud container": nc, "Database container": db} {
		if v != "" && !nameRe.MatchString(v) {
			return "", "", "", fmt.Errorf("%s name can only use letters, numbers, dot, dash and underscore", label)
		}
	}
	if ncURL != "" {
		u, err := url.Parse(ncURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return "", "", "", fmt.Errorf("Nextcloud address must look like http://localhost:7580")
		}
		ncURL = strings.TrimRight(ncURL, "/")
	}
	return nc, ncURL, db, nil
}

// printDoctor is the `sharetunnel doctor` command (run it with docker exec).
func printDoctor(cfg Config) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rep := runChecks(ctx, cfg)
	mark := map[string]string{"ok": "[ OK ]", "warn": "[WARN]", "fail": "[FAIL]"}
	for _, c := range rep.Checks {
		fmt.Printf("%s %s: %s\n", mark[c.Status], c.Title, c.Detail)
		if c.Fix != "" && c.Status != "ok" {
			fmt.Printf("       -> %s\n", c.Fix)
		}
	}
	if len(rep.Nextcloud) > 0 {
		fmt.Println("\nNextcloud containers found:", strings.Join(rep.Nextcloud, ", "))
	}
	if len(rep.Postgres) > 0 {
		fmt.Println("PostgreSQL containers found:", strings.Join(rep.Postgres, ", "))
	}
	if rep.SuggestURL != "" {
		fmt.Println("Nextcloud seems to listen on:", rep.SuggestURL)
	}
	if rep.Ready {
		fmt.Println("\nAll set. Open the panel and press Start.")
	} else {
		fmt.Println("\nFix the [FAIL] items above (or set them on the panel's Setup page), then run this again.")
	}
	return rep.Ready
}
