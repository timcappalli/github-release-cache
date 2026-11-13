package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// NewHandler builds the HTTP handler serving /healthz and cached release files from dataDir.
func NewHandler(dataDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
		}
	})

	fileServer := http.FileServer(noDotFS{root: http.Dir(dataDir)})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if path.Base(r.URL.Path) == "releases.json" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})

	return mux
}

// NewServer creates an *http.Server with sensible timeouts.
func NewServer(addr, dataDir string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           NewHandler(dataDir),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

type noDotFS struct {
	root http.FileSystem
}

func (f noDotFS) Open(name string) (http.File, error) {
	if hasDotSegment(name) {
		return nil, os.ErrNotExist
	}
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	return noDotFile{File: file}, nil
}

type noDotFile struct {
	http.File
}

func (f noDotFile) Readdir(count int) ([]fs.FileInfo, error) {
	entries, err := f.File.Readdir(count)
	filtered := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			filtered = append(filtered, entry)
		}
	}
	return filtered, err
}

func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(path.Clean("/"+p), "/") {
		if strings.HasPrefix(seg, ".") && seg != "" {
			return true
		}
	}
	return false
}
