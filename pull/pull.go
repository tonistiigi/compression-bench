// Package pull fetches benchmark result artifacts produced by CI from the GitHub
// REST API directly (no gh CLI) and extracts the result JSON into the local
// results directory, so `compbench report` can merge local and CI runs.
package pull

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// DefaultBaseURL is the GitHub REST API root.
const DefaultBaseURL = "https://api.github.com"

// artifactPrefix matches the names the CI workflow uploads.
const artifactPrefix = "compbench-results-"

// Artifact is the subset of a GitHub Actions artifact we use.
type Artifact struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	CreatedAt          time.Time `json:"created_at"`
	Expired            bool      `json:"expired"`
	ArchiveDownloadURL string    `json:"archive_download_url"`
}

// Client talks to the GitHub REST API for one repository.
type Client struct {
	Repo    string // "owner/repo"
	Token   string // optional; sent as a Bearer token when set
	BaseURL string // defaults to DefaultBaseURL; overridden in tests
	HTTP    *http.Client
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpc().Do(req)
	return resp, errors.WithStack(err)
}

// ListArtifacts returns all artifacts for the repo, following pagination.
func (c *Client) ListArtifacts(ctx context.Context) ([]Artifact, error) {
	var all []Artifact
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/actions/artifacts?per_page=100&page=%d", c.base(), c.Repo, page)
		resp, err := c.get(ctx, url)
		if err != nil {
			return nil, err
		}
		body, err := readClose(resp)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, errors.Errorf("list artifacts: %s: %s", resp.Status, snippet(body))
		}
		var payload struct {
			TotalCount int        `json:"total_count"`
			Artifacts  []Artifact `json:"artifacts"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, errors.Wrap(err, "decode artifacts")
		}
		all = append(all, payload.Artifacts...)
		if len(payload.Artifacts) == 0 || len(all) >= payload.TotalCount {
			break
		}
	}
	return all, nil
}

// DownloadZip downloads an artifact's zip bytes. The GitHub endpoint redirects to
// blob storage with a signed URL; the default client follows the redirect and the
// signed URL needs no auth header.
func (c *Client) DownloadZip(ctx context.Context, a Artifact) ([]byte, error) {
	resp, err := c.get(ctx, a.ArchiveDownloadURL)
	if err != nil {
		return nil, err
	}
	body, err := readClose(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("download %q: %s: %s", a.Name, resp.Status, snippet(body))
	}
	return body, nil
}

// Run lists artifacts, keeps the most recent n non-expired ones whose names match
// the CI prefix, downloads them, and extracts their *.json into resultsDir
// (skipping files already present). It returns the number of files written.
func Run(ctx context.Context, c *Client, resultsDir string, n int) (int, error) {
	arts, err := c.ListArtifacts(ctx)
	if err != nil {
		return 0, err
	}
	var matched []Artifact
	for _, a := range arts {
		if !a.Expired && strings.HasPrefix(a.Name, artifactPrefix) {
			matched = append(matched, a)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].CreatedAt.After(matched[j].CreatedAt) })
	if n > 0 && len(matched) > n {
		matched = matched[:n]
	}

	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return 0, errors.WithStack(err)
	}
	// Confine all extraction to resultsDir via a root, so a crafted artifact
	// (zip-slip) can't write elsewhere.
	root, err := os.OpenRoot(resultsDir)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	defer root.Close()

	written := 0
	for _, a := range matched {
		zipBytes, err := c.DownloadZip(ctx, a)
		if err != nil {
			return written, err
		}
		cnt, err := extractJSON(root, zipBytes)
		if err != nil {
			return written, errors.Wrapf(err, "extract %q", a.Name)
		}
		written += cnt
	}
	return written, nil
}

// extractJSON writes every *.json entry in the zip into root, skipping any whose
// basename already exists (results files are uniquely named per run).
func extractJSON(root *os.Root, zipBytes []byte) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return 0, errors.WithStack(err)
	}
	written := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		name := filepath.Base(f.Name)
		if _, err := root.Stat(name); err == nil {
			continue // already have it
		}
		if err := writeZipEntry(root, f, name); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

func writeZipEntry(root *os.Root, f *zip.File, name string) error {
	rc, err := f.Open()
	if err != nil {
		return errors.WithStack(err)
	}
	defer rc.Close()
	out, err := root.Create(name)
	if err != nil {
		return errors.WithStack(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(out.Close())
}

func readClose(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, errors.WithStack(err)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
