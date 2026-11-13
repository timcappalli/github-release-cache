package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/timcappalli/github-release-cache/internal/config"
	"github.com/timcappalli/github-release-cache/internal/github"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

type handlerRoundTripper struct {
	handler http.Handler
}

func (rt handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func TestSyncTemplatesAndPruning(t *testing.T) {
	var currentTag atomic.Value
	currentTag.Store("v1.0.0")

	baseURL := "http://mock-github.local"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag := currentTag.Load().(string)
		switch r.URL.Path {
		case "/repos/jxlarrea/kiosk-satellite/releases":
			etag := fmt.Sprintf(`"%s"`, tag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/json")
			releases := []map[string]any{
				{
					"id":         999,
					"tag_name":   "v9.9.9-rc1",
					"draft":      false,
					"prerelease": true,
					"assets":     []any{},
				},
				{
					"id":         100,
					"tag_name":   tag,
					"draft":      false,
					"prerelease": false,
					"assets": []map[string]any{
						{
							"id":                   1,
							"name":                 fmt.Sprintf("kiosk-satellite-%s.apk", tag),
							"size":                 len("apk-" + tag),
							"browser_download_url": baseURL + "/download/" + tag + "/main.apk",
						},
						{
							"id":                   2,
							"name":                 fmt.Sprintf("kiosk-satellite-%s.arm64-v8a.apk", tag),
							"size":                 len("arm64-" + tag),
							"browser_download_url": baseURL + "/download/" + tag + "/arm64.apk",
						},
						{
							"id":                   3,
							"name":                 "source-notes.txt",
							"size":                 4,
							"browser_download_url": baseURL + "/download/" + tag + "/notes.txt",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(releases)
		default:
			if r.URL.Path == "/download/"+tag+"/main.apk" {
				_, _ = io.WriteString(w, "apk-"+tag)
				return
			}
			if r.URL.Path == "/download/"+tag+"/arm64.apk" {
				_, _ = io.WriteString(w, "arm64-"+tag)
				return
			}
			http.NotFound(w, r)
		}
	})

	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpClient := &http.Client{Transport: handlerRoundTripper{handler: handler}}
	client := github.NewClientWithHTTP(baseURL, httpClient)

	repoCfg := config.RepoConfig{
		Repo:               "jxlarrea/kiosk-satellite",
		Path:               "/",
		Files:              []string{"kiosk-satellite-{{.Tag}}.apk", "kiosk-satellite-{{.Tag}}.arm64-*.apk"},
		KeepReleases:       intPtr(2),
		IncludePrereleases: boolPtr(false),
		IncludeDrafts:      boolPtr(false),
		RewriteURLs:        boolPtr(true),
		BaseURL:            "http://local-cache:8080",
		PerPage:            30,
	}

	s := New(repoCfg, dataDir, client, logger)
	ctx := context.Background()

	// Sync 1: v1.0.0
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync v1.0.0 failed: %v", err)
	}
	assertFileExists(t, filepath.Join(dataDir, "releases.json"))
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.0.0.apk"))
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.0.0.arm64-v8a.apk"))
	assertFileNotExists(t, filepath.Join(dataDir, "source-notes.txt"))

	// Verify URL rewriting
	rawReleases, err := os.ReadFile(filepath.Join(dataDir, "releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []github.Release
	if err := json.Unmarshal(rawReleases, &parsed); err != nil {
		t.Fatal(err)
	}
	wantURL := "http://local-cache:8080/kiosk-satellite-v1.0.0.apk"
	if parsed[1].Assets[0].BrowserDownloadURL != wantURL {
		t.Errorf("rewritten URL = %q, want %q", parsed[1].Assets[0].BrowserDownloadURL, wantURL)
	}

	// Sync 1b: 304 Not Modified
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync 304 failed: %v", err)
	}

	// Sync 2: v1.1.0 (kept=2 -> v1.0.0 and v1.1.0 both kept)
	currentTag.Store("v1.1.0")
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync v1.1.0 failed: %v", err)
	}
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.0.0.apk"))
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.1.0.apk"))

	// Sync 3: v1.2.0 (kept=2 -> v1.0.0 should be pruned!)
	currentTag.Store("v1.2.0")
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync v1.2.0 failed: %v", err)
	}
	assertFileNotExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.0.0.apk"))
	assertFileNotExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.0.0.arm64-v8a.apk"))
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.1.0.apk"))
	assertFileExists(t, filepath.Join(dataDir, "kiosk-satellite-v1.2.0.apk"))
}

func TestFileContainsFilter(t *testing.T) {
	baseURL := "http://mock-github.local"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/app/releases":
			w.Header().Set("Content-Type", "application/json")
			releases := []map[string]any{
				{
					"id":       1,
					"tag_name": "v2.0.0",
					"assets": []map[string]any{
						{"id": 1, "name": "app-v2.0.0-arm64-v8a.apk", "size": 5, "browser_download_url": baseURL + "/dl/1"},
						{"id": 2, "name": "app-v2.0.0-x86_64.apk", "size": 5, "browser_download_url": baseURL + "/dl/2"},
						{"id": 3, "name": "app-v2.0.0.dmg", "size": 5, "browser_download_url": baseURL + "/dl/3"},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(releases)
		default:
			_, _ = io.WriteString(w, "12345")
		}
	})

	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := github.NewClientWithHTTP(baseURL, &http.Client{Transport: handlerRoundTripper{handler: handler}})

	s := New(config.RepoConfig{
		Repo:         "owner/app",
		Path:         "/",
		FileContains: []string{"arm64-v8a", ".dmg"},
		PerPage:      30,
	}, dataDir, client, logger)

	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	assertFileExists(t, filepath.Join(dataDir, "app-v2.0.0-arm64-v8a.apk"))
	assertFileExists(t, filepath.Join(dataDir, "app-v2.0.0.dmg"))
	assertFileNotExists(t, filepath.Join(dataDir, "app-v2.0.0-x86_64.apk"))
}

func assertFileExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("expected file %q to exist: %v", p, err)
	}
}

func assertFileNotExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("expected file %q to be absent, got err=%v", p, err)
	}
}
