package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// TimeOfDay represents a 24-hour wall-clock time.
type TimeOfDay struct {
	Hour   int
	Minute int
	Second int
}

// String formats TimeOfDay as HH:MM:SS.
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d:%02d", t.Hour, t.Minute, t.Second)
}

// ParseTimeOfDay parses a 24-hour time string in HH:MM:SS or HH:MM format.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"15:04:05", "15:04"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			return TimeOfDay{
				Hour:   parsed.Hour(),
				Minute: parsed.Minute(),
				Second: parsed.Second(),
			}, nil
		}
	}
	return TimeOfDay{}, fmt.Errorf("invalid time of day %q: expected 24-hour HH:MM:SS or HH:MM", s)
}

// Config holds the application configuration.
type Config struct {
	ListenAddr    string        `json:"listen_addr"`
	DataDir       string        `json:"data_dir"`
	CheckInterval time.Duration `json:"-"`
	CheckEvery    string        `json:"check_interval,omitempty"`
	CheckTime     TimeOfDay     `json:"-"`
	CheckTimeStr  string        `json:"check_time,omitempty"`
	GitHubToken   string        `json:"github_token,omitempty"`
	GitHubAPIURL  string        `json:"github_api_url,omitempty"`
	LogLevel      string        `json:"log_level,omitempty"`
	Repos         []RepoConfig  `json:"repos"`
}

// NextCheck returns the next scheduled check time strictly after now,
// aligned to CheckTime in now's location and stepping by CheckInterval.
func (c *Config) NextCheck(now time.Time) time.Time {
	anchor := time.Date(
		now.Year(), now.Month(), now.Day(),
		c.CheckTime.Hour, c.CheckTime.Minute, c.CheckTime.Second, 0,
		now.Location(),
	)

	if !anchor.After(now) {
		steps := now.Sub(anchor)/c.CheckInterval + 1
		return anchor.Add(steps * c.CheckInterval)
	}

	stepsBack := anchor.Sub(now) / c.CheckInterval
	candidate := anchor.Add(-stepsBack * c.CheckInterval)
	if !candidate.After(now) {
		candidate = candidate.Add(c.CheckInterval)
	}
	return candidate
}

// RepoConfig holds configuration for a single GitHub repository to cache.
type RepoConfig struct {
	Repo               string   `json:"repo"`
	Path               string   `json:"path,omitempty"`
	Files              []string `json:"files,omitempty"`
	FileContains       []string `json:"file_contains,omitempty"`
	KeepReleases       *int     `json:"keep_releases,omitempty"`
	IncludePrereleases *bool    `json:"include_prereleases,omitempty"`
	IncludeDrafts      *bool    `json:"include_drafts,omitempty"`
	RewriteURLs        *bool    `json:"rewrite_urls,omitempty"`
	BaseURL            string   `json:"base_url,omitempty"`
	PerPage            int      `json:"per_page,omitempty"`
	Token              string   `json:"github_token,omitempty"`
}

// EffectiveKeepReleases returns the number of releases to retain (<=0 means unlimited).
func (r RepoConfig) EffectiveKeepReleases() int {
	if r.KeepReleases == nil {
		return 3
	}
	return *r.KeepReleases
}

// EffectiveIncludePrereleases returns whether prereleases should be considered.
func (r RepoConfig) EffectiveIncludePrereleases() bool {
	if r.IncludePrereleases == nil {
		return false
	}
	return *r.IncludePrereleases
}

// EffectiveIncludeDrafts returns whether drafts should be considered.
func (r RepoConfig) EffectiveIncludeDrafts() bool {
	if r.IncludeDrafts == nil {
		return false
	}
	return *r.IncludeDrafts
}

// EffectiveRewriteURLs returns whether browser_download_url in releases.json should be rewritten.
func (r RepoConfig) EffectiveRewriteURLs() bool {
	if r.RewriteURLs == nil {
		return false
	}
	return *r.RewriteURLs
}

// OwnerAndName splits "owner/repo" into owner and repo name.
func (r RepoConfig) OwnerAndName() (string, string) {
	owner, name, _ := strings.Cut(r.Repo, "/")
	return owner, name
}

// Load reads configuration from CONFIG_FILE (if set) or environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:    envOrDefault("LISTEN_ADDR", ":8080"),
		DataDir:       envOrDefault("DATA_DIR", "/data"),
		CheckInterval: time.Hour,
		CheckTime:     TimeOfDay{Hour: 12, Minute: 0, Second: 0},
		GitHubToken:   os.Getenv("GITHUB_TOKEN"),
		GitHubAPIURL:  strings.TrimRight(envOrDefault("GITHUB_API_URL", "https://api.github.com"), "/"),
		LogLevel:      strings.ToLower(envOrDefault("LOG_LEVEL", "info")),
	}

	if intervalStr := os.Getenv("CHECK_INTERVAL"); intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			return nil, fmt.Errorf("invalid CHECK_INTERVAL %q: %w", intervalStr, err)
		}
		cfg.CheckInterval = d
	}

	if timeStr := os.Getenv("CHECK_TIME"); timeStr != "" {
		tod, err := ParseTimeOfDay(timeStr)
		if err != nil {
			return nil, fmt.Errorf("invalid CHECK_TIME %q: %w", timeStr, err)
		}
		cfg.CheckTime = tod
	}

	if configFile := os.Getenv("CONFIG_FILE"); configFile != "" {
		if err := loadFile(configFile, cfg); err != nil {
			return nil, err
		}
	} else {
		repoCfg, err := repoFromEnv()
		if err != nil {
			return nil, err
		}
		cfg.Repos = []RepoConfig{repoCfg}
	}

	if err := cfg.validateAndNormalize(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadFile(filePath string, cfg *Config) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading config file %q: %w", filePath, err)
	}

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return fmt.Errorf("parsing config file %q: %w", filePath, err)
	}

	if fileCfg.ListenAddr != "" {
		cfg.ListenAddr = fileCfg.ListenAddr
	}
	if fileCfg.DataDir != "" {
		cfg.DataDir = fileCfg.DataDir
	}
	if fileCfg.CheckEvery != "" {
		d, err := time.ParseDuration(fileCfg.CheckEvery)
		if err != nil {
			return fmt.Errorf("invalid check_interval %q: %w", fileCfg.CheckEvery, err)
		}
		cfg.CheckInterval = d
	}
	if fileCfg.CheckTimeStr != "" {
		tod, err := ParseTimeOfDay(fileCfg.CheckTimeStr)
		if err != nil {
			return fmt.Errorf("invalid check_time %q: %w", fileCfg.CheckTimeStr, err)
		}
		cfg.CheckTime = tod
	}
	if fileCfg.GitHubToken != "" {
		cfg.GitHubToken = fileCfg.GitHubToken
	}
	if fileCfg.GitHubAPIURL != "" {
		cfg.GitHubAPIURL = strings.TrimRight(fileCfg.GitHubAPIURL, "/")
	}
	if fileCfg.LogLevel != "" {
		cfg.LogLevel = strings.ToLower(fileCfg.LogLevel)
	}
	cfg.Repos = fileCfg.Repos
	return nil
}

func repoFromEnv() (RepoConfig, error) {
	repo := strings.TrimSpace(os.Getenv("GITHUB_REPO"))
	if repo == "" {
		return RepoConfig{}, errors.New("GITHUB_REPO (or CONFIG_FILE) must be set")
	}

	files := parseCSVEnv("FILES")
	fileContains := parseCSVEnv("FILE_CONTAINS")

	keepReleases := 3
	if keepEnv := strings.TrimSpace(os.Getenv("KEEP_RELEASES")); keepEnv != "" {
		v, err := strconv.Atoi(keepEnv)
		if err != nil {
			return RepoConfig{}, fmt.Errorf("invalid KEEP_RELEASES %q: %w", keepEnv, err)
		}
		keepReleases = v
	}

	includePrereleases, err := parseBoolEnv("INCLUDE_PRERELEASES", false)
	if err != nil {
		return RepoConfig{}, err
	}
	includeDrafts, err := parseBoolEnv("INCLUDE_DRAFTS", false)
	if err != nil {
		return RepoConfig{}, err
	}
	rewriteURLs, err := parseBoolEnv("REWRITE_URLS", false)
	if err != nil {
		return RepoConfig{}, err
	}

	perPage := 30
	if ppEnv := strings.TrimSpace(os.Getenv("PER_PAGE")); ppEnv != "" {
		v, err := strconv.Atoi(ppEnv)
		if err != nil {
			return RepoConfig{}, fmt.Errorf("invalid PER_PAGE %q: %w", ppEnv, err)
		}
		perPage = v
	}

	return RepoConfig{
		Repo:               repo,
		Path:               os.Getenv("HTTP_PATH"),
		Files:              files,
		FileContains:       fileContains,
		KeepReleases:       &keepReleases,
		IncludePrereleases: &includePrereleases,
		IncludeDrafts:      &includeDrafts,
		RewriteURLs:        &rewriteURLs,
		BaseURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("BASE_URL")), "/"),
		PerPage:            perPage,
	}, nil
}

func (c *Config) validateAndNormalize() error {
	if c.CheckInterval <= 0 {
		return fmt.Errorf("check interval must be positive, got %s", c.CheckInterval)
	}
	if len(c.Repos) == 0 {
		return errors.New("at least one repository must be configured")
	}

	seenPaths := make(map[string]struct{}, len(c.Repos))
	for i := range c.Repos {
		r := &c.Repos[i]
		owner, name, ok := strings.Cut(r.Repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid repo %q: expected owner/repo", r.Repo)
		}

		if r.PerPage == 0 {
			r.PerPage = 30
		}
		if r.PerPage < 1 || r.PerPage > 100 {
			return fmt.Errorf("repo %s: per_page must be between 1 and 100, got %d", r.Repo, r.PerPage)
		}

		if r.Token == "" {
			r.Token = c.GitHubToken
		}

		if r.Path == "" {
			if len(c.Repos) == 1 {
				r.Path = "/"
			} else {
				r.Path = "/" + name + "/"
			}
		} else {
			r.Path = normalizePath(r.Path)
		}

		if _, exists := seenPaths[r.Path]; exists {
			return fmt.Errorf("duplicate serve path %q", r.Path)
		}
		seenPaths[r.Path] = struct{}{}
		r.BaseURL = strings.TrimRight(r.BaseURL, "/")
	}

	return nil
}

func normalizePath(p string) string {
	cleaned := path.Clean("/" + p)
	if cleaned == "/" {
		return "/"
	}
	return cleaned + "/"
}

func parseBoolEnv(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return b, nil
}

func parseCSVEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
