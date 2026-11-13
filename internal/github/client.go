package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const userAgent = "github-release-cache/1.0"

// Release represents the relevant fields of a GitHub release.
type Release struct {
	ID          int64   `json:"id"`
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Asset represents a downloadable asset attached to a GitHub release.
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// FetchResult holds the raw JSON and parsed releases from GitHub.
type FetchResult struct {
	RawJSON     []byte
	Releases    []Release
	ETag        string
	NotModified bool
}

// Client interacts with the GitHub Releases API.
type Client struct {
	apiBaseURL     string
	apiHTTPClient  *http.Client
	fileHTTPClient *http.Client
}

// NewClient creates a new GitHub API client.
func NewClient(apiBaseURL string) *Client {
	return &Client{
		apiBaseURL: apiBaseURL,
		apiHTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		fileHTTPClient: &http.Client{
			Timeout: 15 * time.Minute,
		},
	}
}

// NewClientWithHTTP creates a Client using a custom HTTP client (useful for testing).
func NewClientWithHTTP(apiBaseURL string, httpClient *http.Client) *Client {
	return &Client{
		apiBaseURL:     apiBaseURL,
		apiHTTPClient:  httpClient,
		fileHTTPClient: httpClient,
	}
}

// FetchReleases fetches the release list for owner/repo.
func (c *Client) FetchReleases(ctx context.Context, owner, repo string, perPage int, token, etag string) (*FetchResult, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d", c.apiBaseURL, owner, repo, perPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.apiHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return &FetchResult{
			ETag:        etag,
			NotModified: true,
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading releases response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, fmt.Errorf("github api returned %s: %s", resp.Status, snippet)
	}

	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("decoding releases json: %w", err)
	}

	return &FetchResult{
		RawJSON:  body,
		Releases: releases,
		ETag:     resp.Header.Get("ETag"),
	}, nil
}

// DownloadAsset downloads a release asset atomically into destPath.
func (c *Client) DownloadAsset(ctx context.Context, asset Asset, token, destPath string) error {
	downloadURL := asset.BrowserDownloadURL
	useAPI := false
	if token != "" && asset.URL != "" {
		downloadURL = asset.URL
		useAPI = true
	}
	if downloadURL == "" {
		return fmt.Errorf("asset %q has no download URL", asset.Name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if useAPI {
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.fileHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %q: %w", asset.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("downloading %q returned %s: %s", asset.Name, resp.Status, string(body))
	}

	dir := filepath.Dir(destPath)
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("writing %q: %w", asset.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp file for %q: %w", asset.Name, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting permissions on %q: %w", asset.Name, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("renaming temp file to %q: %w", destPath, err)
	}

	cleanup = false
	return nil
}
