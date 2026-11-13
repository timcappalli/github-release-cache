package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("GITHUB_REPO", "jxlarrea/kiosk-satellite")
	t.Setenv("CHECK_INTERVAL", "30m")
	t.Setenv("FILES", "kiosk-satellite-{{.Tag}}.apk, kiosk-satellite-{{.Tag}}.arm64-v8a.apk")
	t.Setenv("FILE_CONTAINS", "arm64-v8a, .dmg")
	t.Setenv("KEEP_RELEASES", "5")
	t.Setenv("INCLUDE_PRERELEASES", "true")
	t.Setenv("INCLUDE_DRAFTS", "false")
	t.Setenv("REWRITE_URLS", "true")
	t.Setenv("BASE_URL", "http://192.168.1.50:8080/")
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("DATA_DIR", "/tmp/cache-test")
	t.Setenv("CONFIG_FILE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.ListenAddr)
	}
	if cfg.CheckInterval != 30*time.Minute {
		t.Errorf("CheckInterval = %s, want 30m", cfg.CheckInterval)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(cfg.Repos))
	}

	r := cfg.Repos[0]
	if r.Path != "/" {
		t.Errorf("Path = %q, want /", r.Path)
	}
	if len(r.Files) != 2 {
		t.Errorf("len(Files) = %d, want 2", len(r.Files))
	}
	if len(r.FileContains) != 2 || r.FileContains[0] != "arm64-v8a" || r.FileContains[1] != ".dmg" {
		t.Errorf("FileContains = %v, want [arm64-v8a .dmg]", r.FileContains)
	}
	if r.EffectiveKeepReleases() != 5 {
		t.Errorf("KeepReleases = %d, want 5", r.EffectiveKeepReleases())
	}
	if !r.EffectiveIncludePrereleases() {
		t.Errorf("IncludePrereleases = false, want true")
	}
	if r.EffectiveIncludeDrafts() {
		t.Errorf("IncludeDrafts = true, want false")
	}
	if !r.EffectiveRewriteURLs() {
		t.Errorf("RewriteURLs = false, want true")
	}
	if r.BaseURL != "http://192.168.1.50:8080" {
		t.Errorf("BaseURL = %q, want http://192.168.1.50:8080", r.BaseURL)
	}
	if cfg.CheckTime.String() != "12:00:00" {
		t.Errorf("default CheckTime = %q, want 12:00:00", cfg.CheckTime.String())
	}
}

func TestCheckTimeAndNextCheck(t *testing.T) {
	t.Setenv("GITHUB_REPO", "jxlarrea/kiosk-satellite")
	t.Setenv("CHECK_INTERVAL", "24h")
	t.Setenv("CHECK_TIME", "03:30:15")
	t.Setenv("CONFIG_FILE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.CheckTime.String() != "03:30:15" {
		t.Fatalf("CheckTime = %q, want 03:30:15", cfg.CheckTime.String())
	}

	loc := time.UTC
	// Before 03:30:15 -> same day
	nowBefore := time.Date(2026, 11, 13, 2, 0, 0, 0, loc)
	wantSameDay := time.Date(2026, 11, 13, 3, 30, 15, 0, loc)
	if got := cfg.NextCheck(nowBefore); !got.Equal(wantSameDay) {
		t.Errorf("NextCheck(before) = %s, want %s", got, wantSameDay)
	}

	// After 03:30:15 -> next day
	nowAfter := time.Date(2026, 11, 13, 15, 0, 0, 0, loc)
	wantNextDay := time.Date(2026, 11, 14, 3, 30, 15, 0, loc)
	if got := cfg.NextCheck(nowAfter); !got.Equal(wantNextDay) {
		t.Errorf("NextCheck(after) = %s, want %s", got, wantNextDay)
	}

	// Sub-24h interval (6h) anchored to 12:00:00
	cfg.CheckTime = TimeOfDay{Hour: 12, Minute: 0, Second: 0}
	cfg.CheckInterval = 6 * time.Hour
	nowAfternoon := time.Date(2026, 11, 13, 15, 35, 0, 0, loc)
	wantSixPM := time.Date(2026, 11, 13, 18, 0, 0, 0, loc)
	if got := cfg.NextCheck(nowAfternoon); !got.Equal(wantSixPM) {
		t.Errorf("NextCheck(6h) = %s, want %s", got, wantSixPM)
	}
}

func TestLoadFromJSONFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	content := `{
		"listen_addr": ":8081",
		"check_interval": "15m",
		"repos": [
			{
				"repo": "owner/app-one",
				"files": ["*.apk"],
				"keep_releases": 2
			},
			{
				"repo": "owner/app-two",
				"path": "/custom",
				"include_prereleases": true
			}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CONFIG_FILE", cfgPath)
	t.Setenv("GITHUB_REPO", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.CheckInterval != 15*time.Minute {
		t.Errorf("CheckInterval = %s, want 15m", cfg.CheckInterval)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(cfg.Repos))
	}
	if cfg.Repos[0].Path != "/app-one/" {
		t.Errorf("Repos[0].Path = %q, want /app-one/", cfg.Repos[0].Path)
	}
	if cfg.Repos[1].Path != "/custom/" {
		t.Errorf("Repos[1].Path = %q, want /custom/", cfg.Repos[1].Path)
	}
	if cfg.Repos[0].EffectiveKeepReleases() != 2 {
		t.Errorf("Repos[0].KeepReleases = %d, want 2", cfg.Repos[0].EffectiveKeepReleases())
	}
	if cfg.Repos[1].EffectiveKeepReleases() != 3 {
		t.Errorf("Repos[1].KeepReleases = %d, want 3 (default)", cfg.Repos[1].EffectiveKeepReleases())
	}
}

func TestLoadValidationErrors(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("GITHUB_REPO", "invalid-repo-no-slash")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid GITHUB_REPO, got nil")
	}
}
