package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/timcappalli/github-release-cache/internal/config"
	"github.com/timcappalli/github-release-cache/internal/github"
)

const (
	releasesFileName = "releases.json"
	stateFileName    = ".cache-state.json"
)

// TemplateData provides variables available in file templates.
type TemplateData struct {
	Tag     string
	Version string
	Owner   string
	Repo    string
}

type cacheState struct {
	ETag     string          `json:"etag,omitempty"`
	Releases []cachedRelease `json:"releases,omitempty"`
}

type cachedRelease struct {
	Tag   string   `json:"tag"`
	Files []string `json:"files"`
}

// Syncer synchronizes a single GitHub repository's releases to disk.
type Syncer struct {
	cfg     config.RepoConfig
	dataDir string
	client  *github.Client
	logger  *slog.Logger
}

// New creates a new Syncer for a repository configuration.
func New(cfg config.RepoConfig, dataDir string, client *github.Client, logger *slog.Logger) *Syncer {
	return &Syncer{
		cfg:     cfg,
		dataDir: dataDir,
		client:  client,
		logger:  logger.With("repo", cfg.Repo, "path", cfg.Path),
	}
}

// TargetDir returns the filesystem directory where this repo's files are stored.
func (s *Syncer) TargetDir() string {
	trimmed := strings.Trim(s.cfg.Path, "/")
	if trimmed == "" {
		return s.dataDir
	}
	return filepath.Join(s.dataDir, filepath.FromSlash(trimmed))
}

// Sync fetches releases from GitHub, downloads matching assets, updates releases.json, and prunes old releases.
func (s *Syncer) Sync(ctx context.Context) error {
	dir := s.TargetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating target directory %q: %w", dir, err)
	}

	state := s.loadState(dir)
	etag := state.ETag
	if _, err := os.Stat(filepath.Join(dir, releasesFileName)); err != nil {
		etag = ""
	}

	owner, name := s.cfg.OwnerAndName()
	res, err := s.client.FetchReleases(ctx, owner, name, s.cfg.PerPage, s.cfg.Token, etag)
	if err != nil {
		return err
	}

	if res.NotModified {
		s.logger.Debug("releases not modified (304)")
		return nil
	}

	releasesBytes := res.RawJSON
	if s.cfg.EffectiveRewriteURLs() {
		rewritten, err := s.rewriteReleaseURLs(res.RawJSON)
		if err != nil {
			return fmt.Errorf("rewriting release URLs: %w", err)
		}
		releasesBytes = rewritten
	}

	latest, ok := s.selectLatestRelease(res.Releases)
	if !ok {
		s.logger.Warn("no matching release found (check draft/prerelease settings)")
		if err := writeFileAtomic(filepath.Join(dir, releasesFileName), releasesBytes); err != nil {
			return fmt.Errorf("writing %s: %w", releasesFileName, err)
		}
		state.ETag = res.ETag
		return s.saveState(dir, state)
	}

	assets, err := s.matchAssets(latest)
	if err != nil {
		return fmt.Errorf("matching assets for %s: %w", latest.TagName, err)
	}

	var cachedFiles []string
	for _, asset := range assets {
		safeName := filepath.Base(asset.Name)
		if safeName == "." || safeName == ".." || safeName == "" {
			continue
		}
		destPath := filepath.Join(dir, safeName)

		if info, err := os.Stat(destPath); err == nil && (asset.Size <= 0 || info.Size() == asset.Size) {
			s.logger.Debug("asset already cached", "tag", latest.TagName, "file", safeName)
			cachedFiles = append(cachedFiles, safeName)
			continue
		}

		s.logger.Info("downloading asset", "tag", latest.TagName, "file", safeName, "size", asset.Size)
		if err := s.client.DownloadAsset(ctx, asset, s.cfg.Token, destPath); err != nil {
			return fmt.Errorf("downloading asset %q: %w", safeName, err)
		}
		cachedFiles = append(cachedFiles, safeName)
	}

	if err := writeFileAtomic(filepath.Join(dir, releasesFileName), releasesBytes); err != nil {
		return fmt.Errorf("writing %s: %w", releasesFileName, err)
	}

	state.ETag = res.ETag
	s.updateAndPrune(dir, &state, latest.TagName, cachedFiles)
	if err := s.saveState(dir, state); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	s.logger.Info("sync complete", "tag", latest.TagName, "files", len(cachedFiles), "kept_releases", len(state.Releases))
	return nil
}

func (s *Syncer) selectLatestRelease(releases []github.Release) (github.Release, bool) {
	includeDrafts := s.cfg.EffectiveIncludeDrafts()
	includePrereleases := s.cfg.EffectiveIncludePrereleases()

	for _, r := range releases {
		if r.Draft && !includeDrafts {
			continue
		}
		if r.Prerelease && !includePrereleases {
			continue
		}
		return r, true
	}
	return github.Release{}, false
}

func (s *Syncer) matchAssets(release github.Release) ([]github.Asset, error) {
	var candidates []github.Asset
	if len(s.cfg.Files) == 0 {
		candidates = release.Assets
	} else {
		owner, name := s.cfg.OwnerAndName()
		data := TemplateData{
			Tag:     release.TagName,
			Version: strings.TrimPrefix(release.TagName, "v"),
			Owner:   owner,
			Repo:    name,
		}

		seen := make(map[string]struct{})
		for _, rawPattern := range s.cfg.Files {
			pattern, err := expandTemplate(rawPattern, data)
			if err != nil {
				return nil, fmt.Errorf("expanding file template %q: %w", rawPattern, err)
			}

			count := 0
			for _, asset := range release.Assets {
				ok, err := filepath.Match(pattern, asset.Name)
				if err != nil {
					return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
				}
				if !ok {
					continue
				}
				count++
				if _, exists := seen[asset.Name]; !exists {
					seen[asset.Name] = struct{}{}
					candidates = append(candidates, asset)
				}
			}
			if count == 0 {
				s.logger.Warn("file pattern matched no assets", "tag", release.TagName, "pattern", pattern)
			}
		}
	}

	if len(s.cfg.FileContains) == 0 {
		return candidates, nil
	}

	var filtered []github.Asset
	for _, asset := range candidates {
		if containsAny(asset.Name, s.cfg.FileContains) {
			filtered = append(filtered, asset)
		}
	}
	if len(filtered) == 0 {
		s.logger.Warn("file_contains filter matched no assets", "tag", release.TagName, "substrings", s.cfg.FileContains)
	}
	return filtered, nil
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func expandTemplate(raw string, data TemplateData) (string, error) {
	tmpl, err := template.New("file").Option("missingkey=error").Parse(raw)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Syncer) rewriteReleaseURLs(raw []byte) ([]byte, error) {
	var releases []map[string]any
	if err := json.Unmarshal(raw, &releases); err != nil {
		return nil, err
	}

	prefix := s.cfg.Path
	if s.cfg.BaseURL != "" {
		prefix = strings.TrimRight(s.cfg.BaseURL, "/") + s.cfg.Path
	}

	for _, rel := range releases {
		assetsRaw, ok := rel["assets"].([]any)
		if !ok {
			continue
		}
		for _, item := range assetsRaw {
			assetMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, ok := assetMap["name"].(string)
			if !ok || name == "" {
				continue
			}
			assetMap["browser_download_url"] = prefix + url.PathEscape(filepath.Base(name))
		}
	}

	return json.Marshal(releases)
}

func (s *Syncer) updateAndPrune(dir string, state *cacheState, tag string, files []string) {
	updated := make([]cachedRelease, 0, len(state.Releases)+1)
	updated = append(updated, cachedRelease{Tag: tag, Files: files})
	for _, r := range state.Releases {
		if r.Tag != tag {
			updated = append(updated, r)
		}
	}

	keep := s.cfg.EffectiveKeepReleases()
	if keep <= 0 || len(updated) <= keep {
		state.Releases = updated
		return
	}

	kept := updated[:keep]
	pruned := updated[keep:]

	keptFiles := make(map[string]struct{})
	for _, r := range kept {
		for _, f := range r.Files {
			keptFiles[f] = struct{}{}
		}
	}

	for _, r := range pruned {
		for _, f := range r.Files {
			if _, stillUsed := keptFiles[f]; stillUsed {
				continue
			}
			target := filepath.Join(dir, f)
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				s.logger.Warn("failed to prune old file", "tag", r.Tag, "file", f, "error", err)
			} else if err == nil {
				s.logger.Info("pruned old release file", "tag", r.Tag, "file", f)
			}
		}
	}

	state.Releases = kept
}

func (s *Syncer) loadState(dir string) cacheState {
	data, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		return cacheState{}
	}
	var st cacheState
	if err := json.Unmarshal(data, &st); err != nil {
		return cacheState{}
	}
	return st
}

func (s *Syncer) saveState(dir string, state cacheState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, stateFileName), data)
}

func writeFileAtomic(destPath string, data []byte) error {
	dir := filepath.Dir(destPath)
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
