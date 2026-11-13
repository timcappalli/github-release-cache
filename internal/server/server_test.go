package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerHidesDotfilesAndServesReleases(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "releases.json"), []byte(`[{"tag_name":"v1.0.0"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.apk"), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cache-state.json"), []byte(`{"secret":"internal"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(dir)

	// /healthz
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}

	// /releases.json
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/releases.json", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/releases.json status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	// /.cache-state.json should be 404
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.cache-state.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/.cache-state.json status = %d, want 404", rec.Code)
	}

	// Directory listing on / should not include .cache-state.json
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, ".cache-state.json") {
		t.Errorf("directory listing leaked .cache-state.json")
	}
	if !strings.Contains(body, "app.apk") {
		t.Errorf("directory listing missing app.apk")
	}
}
