package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFriendly(t *testing.T) {
	cases := map[string]string{
		"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock": "not allowed to use Docker",
		"failed to connect to the docker API at unix:///var/run/docker.sock: no such file or directory":        "Can't reach Docker",
		"Error response from daemon: No such container: nextcloud":                                             "doesn't exist",
		"Error response from daemon: container abc is not running":                                             "stopped",
		`ERROR: relation "oc_share" does not exist`:                                                            "PostgreSQL",
		"exec: \"docker\": executable file not found in $PATH":                                                 "missing",
	}
	for in, want := range cases {
		if got := friendly(in); !strings.Contains(got, want) {
			t.Errorf("friendly(%q) = %q, want it to contain %q", in, got, want)
		}
	}
	if got := friendly("line one\nline two"); got != "line one" {
		t.Errorf("unknown errors should show the first line, got %q", got)
	}
}

func TestValidateConfigInput(t *testing.T) {
	if _, u, _, err := validateConfigInput("nextcloud", "http://localhost:7580/", "db-nextcloud"); err != nil || u != "http://localhost:7580" {
		t.Errorf("good input rejected or not cleaned: %q %v", u, err)
	}
	if _, _, _, err := validateConfigInput("", "", ""); err != nil {
		t.Errorf("empty (reset to default) must be allowed: %v", err)
	}
	for _, bad := range [][3]string{
		{"-rm", "", ""}, {"a b", "", ""}, {"nc;ls", "", ""}, {"", "localhost:7580", ""}, {"", "ftp://x", ""}, {"", "http://x/path", ""}, {"", "", "$(id)"},
	} {
		if _, _, _, err := validateConfigInput(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("input %v should be rejected", bad)
		}
	}
}

// fakeDocker puts a tiny "docker" script on PATH so the checks can be tested without Docker.
func fakeDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflared"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("DATA_DIR", t.TempDir())
}

func status(rep Report, id string) string {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c.Status
		}
	}
	return "missing"
}

func TestNoDockerSocket(t *testing.T) {
	fakeDocker(t, `echo "failed to connect to the docker API at unix:///var/run/docker.sock: no such file or directory"; exit 1`)
	rep := runChecks(context.Background(), loadConfig())
	if rep.Ready || status(rep, "docker") != "fail" {
		t.Fatalf("expected docker failure, got %+v", rep.Checks)
	}
	if !strings.Contains(rep.Checks[0].Detail, "Can't reach Docker") {
		t.Errorf("unfriendly message: %q", rep.Checks[0].Detail)
	}
}

func TestNextcloudNotInstalledOnServer(t *testing.T) {
	fakeDocker(t, `
case "$1" in
  version) echo 27.0.0 ;;
  ps) echo "jellyfin|jellyfin/jellyfin"; echo "other-db|postgres:16" ;;
  inspect) echo "Error: No such object: $4"; exit 1 ;;
esac`)
	rep := runChecks(context.Background(), loadConfig())
	if rep.Ready || status(rep, "nextcloud") != "fail" {
		t.Fatalf("expected Nextcloud failure, got %+v", rep.Checks)
	}
	if status(rep, "database") != "warn" {
		t.Errorf("a missing database must only warn, got %q", status(rep, "database"))
	}
	if criticalProblem(context.Background(), loadConfig()) == "" {
		t.Error("criticalProblem should explain why the tunnel can't start")
	}
}

func TestDetectsContainersAndSkipsCron(t *testing.T) {
	fakeDocker(t, `echo "nextcloud|nextcloud:29"; echo "big-bear-nextcloud-cron-1|nextcloud:29"; echo "immich-postgres|postgres:16"; echo "db-nextcloud|postgres:16"`)
	nc, pg := detect(context.Background())
	if len(nc) != 1 || nc[0] != "nextcloud" {
		t.Errorf("nextcloud candidates: %v", nc)
	}
	if len(pg) != 2 || pg[0] != "db-nextcloud" {
		t.Errorf("postgres candidates should list the nextcloud one first: %v", pg)
	}
}

func TestSavedSettingsOverrideEnv(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	cfg := loadConfig()
	updateSettings(cfg, func(s *Settings) { s.NCContainer, s.NCURL = "mycloud", "http://localhost:8080" })
	e := effectiveConfig(cfg)
	if e.NCContainer != "mycloud" || e.NCURL != "http://localhost:8080" || e.DBContainer != "db-nextcloud" {
		t.Errorf("effective config wrong: %+v", e)
	}
}
