package main

import (
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	cur, _ := parseVer("0.1.9")
	for tag, want := range map[string]bool{"v0.1.10": true, "v0.2.0": true, "v1.0.0": true, "v0.1.9": false, "v0.1.8": false} {
		lat, ok := parseVer(tag)
		if !ok || newer(lat, cur) != want {
			t.Errorf("newer(%s, 0.1.9) want %v", tag, want)
		}
	}
	for _, bad := range []string{"edge", "dev", "1.2", "v1.x.0"} {
		if _, ok := parseVer(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestNextRef(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/o/sharetunnel:latest": "ghcr.io/o/sharetunnel:latest",
		"ghcr.io/o/sharetunnel:edge":   "ghcr.io/o/sharetunnel:edge",
		"ghcr.io/o/sharetunnel:0.1.0":  "ghcr.io/o/sharetunnel:0.2.0",
		"ghcr.io/o/sharetunnel":        "ghcr.io/o/sharetunnel",
		"localhost:5000/sharetunnel":   "localhost:5000/sharetunnel",
	}
	for in, want := range cases {
		if got := nextRef(in, "v0.2.0"); got != want {
			t.Errorf("nextRef(%s)=%s want %s", in, got, want)
		}
	}
}

func TestRunArgs(t *testing.T) {
	var i inspectInfo
	i.Config.Env = []string{"PATH=/bin", "PANEL_PASSWORD=x", "DATA_DIR=/data"}
	i.HostConfig.Binds = []string{"/a:/a", "/b:/data"}
	i.HostConfig.NetworkMode = "host"
	i.HostConfig.RestartPolicy.Name = "unless-stopped"
	got := strings.Join(runArgs(i, "sharetunnel", "img:1"), " ")
	want := "run -d --name sharetunnel --restart unless-stopped --network host -e PANEL_PASSWORD=x -e DATA_DIR=/data -v /a:/a -v /b:/data img:1"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}
