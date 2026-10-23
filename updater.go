package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const helperName = "sharetunnel-updater"

// ---------- version helpers ----------

func parseVer(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func newer(latest, cur [3]int) bool {
	for i := range latest {
		if latest[i] != cur[i] {
			return latest[i] > cur[i]
		}
	}
	return false
}

// A container pinned to a version tag follows the new version; :latest / :edge stay as they are.
func nextRef(ref, newVer string) string {
	slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":")
	if colon <= slash {
		return ref
	}
	if _, ok := parseVer(ref[colon+1:]); ok {
		return ref[:colon] + ":" + strings.TrimPrefix(newVer, "v")
	}
	return ref
}

// ---------- checking GitHub for a new release ----------

type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Dev       bool   `json:"dev"` // running a dev/edge build, no version to compare
	Notes     string `json:"notes"`
	URL       string `json:"url"`
	CheckedAt int64  `json:"checked_at"`
	Error     string `json:"error"`
	Mode      string `json:"mode"`
	Hour      int    `json:"hour"`
	Updating  bool   `json:"updating"`
}

type Updater struct {
	cfg  Config
	m    *Manager
	mu   sync.Mutex
	info UpdateInfo
}

func NewUpdater(cfg Config, m *Manager) *Updater { return &Updater{cfg: cfg, m: m} }

func (u *Updater) Info() UpdateInfo {
	s := loadSettings(u.cfg)
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.info
	i.Current = version
	_, ok := parseVer(version)
	i.Dev = !ok
	i.Mode, i.Hour = s.UpdateMode, s.AutoHour
	return i
}

func (u *Updater) fail(msg string) {
	u.mu.Lock()
	u.info.Error, u.info.CheckedAt = msg, time.Now().Unix()
	u.mu.Unlock()
}

func (u *Updater) Check() {
	if u.cfg.Demo {
		u.demoCheck()
		return
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+u.cfg.Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sharetunnel/"+version)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		u.fail("Could not reach GitHub: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		u.fail("No releases published yet.")
		return
	}
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		u.fail("GitHub is limiting requests right now, try again in a few minutes.")
		return
	}
	if resp.StatusCode != 200 {
		u.fail(fmt.Sprintf("GitHub answered %d", resp.StatusCode))
		return
	}
	var rel struct {
		Tag  string `json:"tag_name"`
		Body string `json:"body"`
		URL  string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		u.fail("Unreadable answer from GitHub")
		return
	}
	cur, okCur := parseVer(version)
	lat, okLat := parseVer(rel.Tag)
	if len(rel.Body) > 2000 {
		rel.Body = rel.Body[:2000] + "…"
	}
	u.mu.Lock()
	u.info.Latest, u.info.Notes, u.info.URL = rel.Tag, rel.Body, rel.URL
	u.info.Available = okCur && okLat && newer(lat, cur)
	u.info.Error, u.info.CheckedAt = "", time.Now().Unix()
	u.mu.Unlock()
}

// ---------- applying: start a helper container that swaps us out ----------

func (u *Updater) Apply() error {
	if u.cfg.Demo {
		return errors.New("updates are disabled in demo mode")
	}
	u.mu.Lock()
	if u.info.Updating {
		u.mu.Unlock()
		return errors.New("an update is already running")
	}
	if !u.info.Available {
		u.mu.Unlock()
		return errors.New("no update available")
	}
	latest := u.info.Latest
	u.info.Updating = true
	u.mu.Unlock()

	reset := func(err error) error {
		u.mu.Lock()
		u.info.Updating = false
		u.mu.Unlock()
		return err
	}
	out, err := exec.Command("docker", "inspect", "-f", "{{.Config.Image}}", u.cfg.SelfContainer).Output()
	if err != nil {
		return reset(fmt.Errorf("cannot find my own container %q (set SELF_CONTAINER): %v", u.cfg.SelfContainer, err))
	}
	ref := strings.TrimSpace(string(out))
	exec.Command("docker", "rm", "-f", helperName).Run()
	b, err := exec.Command("docker", "run", "-d", "--name", helperName, "--network", "host",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-e", "SELF_CONTAINER="+u.cfg.SelfContainer, "-e", "NEW_VERSION="+latest, "-e", "OLD_VERSION="+version,
		ref, "apply-update").CombinedOutput()
	if err != nil {
		return reset(fmt.Errorf("could not start updater: %s", strings.TrimSpace(string(b))))
	}
	u.m.logf("Updating %s -> %s (helper started)", version, latest)
	return nil
}

// loop: check every 6h, and in auto mode apply at the chosen hour.
func (u *Updater) loop(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s := loadSettings(u.cfg)
		if s.UpdateMode != "off" {
			u.mu.Lock()
			stale := time.Since(time.Unix(u.info.CheckedAt, 0)) > 6*time.Hour
			u.mu.Unlock()
			if stale {
				u.Check()
			}
			info := u.Info()
			if s.UpdateMode == "auto" && info.Available && time.Now().Hour() == s.AutoHour && s.AutoTried != info.Latest {
				updateSettings(u.cfg, func(s *Settings) { s.AutoTried = info.Latest }) // never retry the same version automatically
				u.m.logf("Auto-update to %s", info.Latest)
				if err := u.Apply(); err != nil {
					u.m.logf("Auto-update failed: %v", err)
				}
			}
		}
		timer.Reset(10 * time.Minute)
	}
}

// After an update, copy the helper's log into our log, then remove it.
func reportLastUpdate(ctx context.Context, m *Manager) {
	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", helperName).Output()
		if err != nil {
			return // no helper, nothing to report
		}
		if strings.TrimSpace(string(out)) == "false" {
			logs, _ := exec.Command("docker", "logs", helperName).CombinedOutput()
			for _, l := range strings.Split(strings.TrimSpace(string(logs)), "\n") {
				m.logf("%s", l)
			}
			exec.Command("docker", "rm", "-f", helperName).Run()
			return
		}
	}
}

// ---------- the helper itself (runs as: sharetunnel apply-update) ----------

type inspectInfo struct {
	Config struct {
		Image string
		Env   []string
	}
	HostConfig struct {
		Binds         []string
		NetworkMode   string
		RestartPolicy struct{ Name string }
	}
}

func inspectContainer(name string) (inspectInfo, error) {
	var arr []inspectInfo
	out, err := exec.Command("docker", "inspect", name).Output()
	if err != nil {
		return inspectInfo{}, err
	}
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) == 0 {
		return inspectInfo{}, errors.New("unreadable inspect output")
	}
	return arr[0], nil
}

// runArgs rebuilds `docker run` so the new container has the same settings as the old one.
func runArgs(i inspectInfo, name, image string) []string {
	args := []string{"run", "-d", "--name", name}
	if p := i.HostConfig.RestartPolicy.Name; p != "" && p != "no" {
		args = append(args, "--restart", p)
	}
	if n := i.HostConfig.NetworkMode; n != "" && n != "default" {
		args = append(args, "--network", n)
	}
	for _, e := range i.Config.Env {
		if !strings.HasPrefix(e, "PATH=") {
			args = append(args, "-e", e)
		}
	}
	for _, b := range i.HostConfig.Binds {
		args = append(args, "-v", b)
	}
	return append(args, image)
}

func waitHealthy(port, oldVer string) (string, bool) {
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			continue
		}
		var h struct{ Version string }
		json.NewDecoder(resp.Body).Decode(&h)
		resp.Body.Close()
		if resp.StatusCode == 200 && h.Version != "" && h.Version != oldVer {
			return h.Version, true
		}
	}
	return "", false
}

func applyUpdate() {
	say := func(f string, a ...any) { fmt.Printf("[updater] "+f+"\n", a...) }
	self := getenv("SELF_CONTAINER", "sharetunnel")
	newVer, oldVer, old := os.Getenv("NEW_VERSION"), os.Getenv("OLD_VERSION"), getenv("SELF_CONTAINER", "sharetunnel")+"-old"

	info, err := inspectContainer(self)
	if err != nil {
		say("cannot inspect %s: %v", self, err)
		os.Exit(1)
	}
	newRef := nextRef(info.Config.Image, newVer)
	say("pulling %s", newRef)
	if out, err := exec.Command("docker", "pull", newRef).CombinedOutput(); err != nil {
		say("pull failed, nothing was changed: %s", strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	port := "9099"
	for _, e := range info.Config.Env {
		if strings.HasPrefix(e, "PANEL_PORT=") {
			port = strings.TrimPrefix(e, "PANEL_PORT=")
		}
	}

	rollback := func() {
		say("ROLLING BACK to the old version")
		exec.Command("docker", "rm", "-f", self).Run()
		exec.Command("docker", "rename", old, self).Run()
		exec.Command("docker", "start", self).Run()
		say("rollback done, old version is running again")
	}

	exec.Command("docker", "rm", "-f", old).Run()
	say("stopping old container")
	if out, err := exec.Command("docker", "stop", "-t", "20", self).CombinedOutput(); err != nil {
		say("could not stop old container: %s", strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	if out, err := exec.Command("docker", "rename", self, old).CombinedOutput(); err != nil {
		say("rename failed: %s", strings.TrimSpace(string(out)))
		exec.Command("docker", "start", self).Run()
		os.Exit(1)
	}
	say("starting new container")
	if out, err := exec.Command("docker", runArgs(info, self, newRef)...).CombinedOutput(); err != nil {
		say("new container failed to start: %s", strings.TrimSpace(string(out)))
		rollback()
		os.Exit(1)
	}
	if v, ok := waitHealthy(port, oldVer); ok {
		exec.Command("docker", "rm", old).Run()
		say("UPDATE OK: now running %s", v)
		return
	}
	say("new version did not become healthy in 75s")
	rollback()
	os.Exit(1)
}
