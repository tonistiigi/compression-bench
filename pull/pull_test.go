package pull

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeGitHub serves a canned artifact list and a zip download, mimicking the
// subset of the GitHub REST API that pull uses. No network.
func fakeGitHub(t *testing.T, arts []map[string]any, zips map[int64][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/actions/artifacts", func(w http.ResponseWriter, req *http.Request) {
		// single page is enough for the test
		if req.URL.Query().Get("page") != "1" {
			json.NewEncoder(w).Encode(map[string]any{"total_count": len(arts), "artifacts": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": len(arts), "artifacts": arts})
	})
	for id, data := range zips {
		data := data
		mux.HandleFunc(fmt.Sprintf("/dl/%d", id), func(w http.ResponseWriter, req *http.Request) {
			w.Write(data)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func zipWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	require.NoError(t, err)
	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestRun(t *testing.T) {
	zips := map[int64][]byte{
		1: zipWith(t, "20260101-000000-ci.json", []byte(`{"schemaVersion":1}`)),
		2: zipWith(t, "ignored.txt", []byte("nope")),
	}
	arts := []map[string]any{
		{"id": 1, "name": "compbench-results-amd64-1", "created_at": "2026-01-01T00:00:00Z", "expired": false},
		{"id": 2, "name": "some-other-artifact", "created_at": "2026-01-02T00:00:00Z", "expired": false},
		{"id": 3, "name": "compbench-results-arm64-0", "created_at": "2026-01-01T00:00:00Z", "expired": true},
	}
	srv := fakeGitHub(t, arts, zips)
	// point each artifact's download URL at the fake server
	for _, a := range arts {
		a["archive_download_url"] = fmt.Sprintf("%s/dl/%v", srv.URL, a["id"])
	}

	c := &Client{Repo: "o/r", BaseURL: srv.URL, HTTP: srv.Client()}
	dir := t.TempDir()

	n, err := Run(context.Background(), c, dir, 0)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the matching, non-expired artifact's json")

	got, err := os.ReadFile(filepath.Join(dir, "20260101-000000-ci.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"schemaVersion":1}`, string(got))

	// Second pull is idempotent: the file already exists, nothing new written.
	n2, err := Run(context.Background(), c, dir, 0)
	require.NoError(t, err)
	require.Equal(t, 0, n2)
}

func TestListArtifactsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := &Client{Repo: "o/r", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := c.ListArtifacts(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Not Found")
}
