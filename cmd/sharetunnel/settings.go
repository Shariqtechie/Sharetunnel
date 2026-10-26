package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings live in DATA_DIR/state.json and survive restarts and updates.
type Settings struct {
	Enabled    bool   `json:"enabled"`     // tunnel on/off (resume after reboot)
	UpdateMode string `json:"update_mode"` // notify | auto | off
	AutoHour   int    `json:"auto_hour"`   // hour of day for auto-update
	AutoTried  string `json:"auto_tried"`  // last version auto-update attempted
	Schema     int    `json:"schema"`

	// Set on the Setup page. Empty = use the environment variable / default.
	NCContainer string `json:"nc_container,omitempty"`
	NCURL       string `json:"nc_url,omitempty"`
	DBContainer string `json:"db_container,omitempty"`
}

var settingsMu sync.Mutex

func readSettings(cfg Config) Settings {
	s := Settings{Enabled: false, UpdateMode: "notify", AutoHour: 4, Schema: 1} // a fresh install waits for the user to press Start
	if b, err := os.ReadFile(filepath.Join(cfg.DataDir, "state.json")); err == nil {
		json.Unmarshal(b, &s) // missing fields keep their defaults
	}
	return s
}

func loadSettings(cfg Config) Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return readSettings(cfg)
}

func updateSettings(cfg Config, fn func(*Settings)) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := readSettings(cfg)
	fn(&s)
	os.MkdirAll(cfg.DataDir, 0o755)
	b, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(cfg.DataDir, "state.json"), b, 0o644)
}

// effectiveConfig applies the values saved on the Setup page over the
// environment defaults. Call it wherever Nextcloud or the database is used,
// so changes take effect without restarting the container.
func effectiveConfig(cfg Config) Config {
	s := loadSettings(cfg)
	if s.NCContainer != "" {
		cfg.NCContainer = s.NCContainer
	}
	if s.NCURL != "" {
		cfg.NCURL = s.NCURL
	}
	if s.DBContainer != "" {
		cfg.DBContainer = s.DBContainer
	}
	return cfg
}
