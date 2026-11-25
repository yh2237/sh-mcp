package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeReleases serves /latest -> tag and the asset with its SHA256SUMS.
func fakeReleases(t *testing.T, tag string, bin []byte, sum string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("sh-mcp-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	if sum == "" {
		s := sha256.Sum256(bin)
		sum = hex.EncodeToString(s[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s *%s\n", sum, asset)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bin)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdate(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "sh-mcp")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := fakeReleases(t, "v1.2.0", []byte("new binary"), "")
	var out bytes.Buffer
	opts := Options{Current: "v1.1.0", BaseURL: srv.URL + "/releases", Exe: exe, Out: &out}
	if err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Errorf("binary = %q", got)
	}
	if !strings.Contains(out.String(), "v1.1.0 to v1.2.0") {
		t.Errorf("output: %s", out.String())
	}

	out.Reset()
	opts.Current = "v1.2.0"
	if err := Run(context.Background(), opts); err != nil || !strings.Contains(out.String(), "already the latest") {
		t.Errorf("same version: %v %s", err, out.String())
	}
}

func TestUpdateChecksumMismatch(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "sh-mcp")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := fakeReleases(t, "v1.2.0", []byte("tampered"), strings.Repeat("0", 64))
	err := Run(context.Background(), Options{Current: "v1.1.0", BaseURL: srv.URL + "/releases", Exe: exe, Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Errorf("a failed update changed the binary: %q", got)
	}
}

func TestUpdateNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases", http.StatusFound) // what GitHub does without releases
	}))
	defer srv.Close()
	err := Run(context.Background(), Options{Current: "dev", BaseURL: srv.URL + "/releases", Exe: filepath.Join(t.TempDir(), "x"), Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "no release found") {
		t.Fatalf("err = %v", err)
	}
}
