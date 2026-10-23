package main

import (
	"context"
	"strings"
	"time"
)

// Demo mode (DEMO=true) lets anyone try the panel without Nextcloud, Docker or a tunnel.

func demoShares() []ShareOut {
	day := int64(86400)
	now := time.Now().Unix()
	in := func(d int) *string {
		t := time.Now().Add(time.Duration(d) * 24 * time.Hour).Format("2006-01-02T15:04:05")
		return &t
	}
	items := []Share{
		{ID: 6, Token: "k3Hn9sQw2LpXaRv", ItemType: "folder", Size: 2_400_000_000, Name: "/Summer Trip 2026 (Photos)", Owner: "alice", HasPW: true, Expiration: in(7), Created: now - 2*day},
		{ID: 5, Token: "Pq81ZxT7mYc4NdE", ItemType: "file", Mimetype: "video/mp4", Size: 842_000_000, Name: "/Videos/Wedding Highlights.mp4", Owner: "alice", HasPW: true, Created: now - 5*day},
		{ID: 4, Token: "wB6tRn0JfUe3GhS", ItemType: "file", Mimetype: "application/pdf", Size: 1_280_000, Name: "/Work/Quarterly Report.pdf", Owner: "bob", Expiration: in(1), Created: now - 6*day},
		{ID: 3, Token: "aD5yVk9CoMz1LqX", ItemType: "folder", Size: 11_800_000_000, Name: "/Backups/Project Archive", Owner: "casaos", HasPW: true, Created: now - 12*day},
		{ID: 2, Token: "nF2uIb7HsTj8EwY", ItemType: "file", Mimetype: "audio/mpeg", Size: 58_000_000, Name: "/Podcasts/Episode 12.mp3", Owner: "bob", Created: now - 20*day},
		{ID: 1, Token: "cG4oPl6KxRa0VmZ", ItemType: "file", Mimetype: "image/jpeg", Size: 4_100_000, Name: "/Pictures/Team Photo.jpg", Owner: "alice", Created: now - 31*day},
	}
	out := make([]ShareOut, 0, len(items))
	for _, s := range items {
		icon, kind := kindOf(s)
		out = append(out, ShareOut{Share: s, Kind: kind, Icon: icon, SizeText: humanSize(s.Size)})
	}
	return out
}

func (m *Manager) demoLoop(ctx context.Context) {
	m.logf("Demo mode: no real tunnel is started and nothing is shared.")
	m.set("starting", "")
	select {
	case <-ctx.Done():
		return
	case <-time.After(1200 * time.Millisecond):
	}
	const u = "https://demo-quiet-river-lake.trycloudflare.com"
	m.logf("Tunnel is up: %s", u)
	m.logf("Nextcloud now trusts %s", strings.TrimPrefix(u, "https://"))
	m.set("running", u)
	<-ctx.Done()
	m.logf("Tunnel stopped")
}

func (u *Updater) demoCheck() {
	u.mu.Lock()
	u.info.Latest = "v9.9.9"
	u.info.Available = true
	u.info.URL = "https://github.com/" + u.cfg.Repo + "/releases"
	u.info.Notes = "## What's new\n* Faster share list\n* Named tunnel support (bring your own domain)\n* Dark and light themes\n\nThis is demo data."
	u.info.Error, u.info.CheckedAt = "", time.Now().Unix()
	u.mu.Unlock()
}
