// Package update replaces the running sh-mcp binary with the latest release
// from GitHub, checked against the release's SHA256SUMS (as install.sh does).
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is where releases are published.
const DefaultBaseURL = "https://github.com/yh2237/sh-mcp/releases"

const maxBinary = 200 << 20

// Options configure an update. BaseURL and Exe exist for tests; empty means
// the real releases and the running executable.
type Options struct {
	Current string // version of the running binary
	BaseURL string
	Exe     string
	Out     io.Writer
}

// Run installs the latest release over the executable unless it already is
// that version. A running server keeps using the old binary until restarted.
func Run(ctx context.Context, o Options) error {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if o.Exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
	}
	client := &http.Client{
		Timeout: 5 * time.Minute,
		// Not following the redirect of /latest reveals its tag.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	tag, err := latestTag(ctx, client, o)
	if err != nil {
		return err
	}
	if tag == o.Current {
		fmt.Fprintf(o.Out, "sh-mcp %s is already the latest release\n", tag)
		return nil
	}
	asset := fmt.Sprintf("sh-mcp-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	fmt.Fprintf(o.Out, "Downloading %s %s\n", asset, tag)
	download := o.BaseURL + "/download/" + tag + "/"
	sums, err := get(ctx, client, o, download+"SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}
	bin, err := get(ctx, client, o, download+asset, maxBinary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s (want %s, got %s); nothing was changed", asset, want, got)
	}
	if err := replace(o.Exe, bin); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Updated %s from %s to %s. Restart sh-mcp to use it.\n", o.Exe, o.Current, tag)
	return nil
}

// latestTag reads the tag that /releases/latest redirects to.
func latestTag(ctx context.Context, client *http.Client, o Options) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, o.BaseURL+"/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "sh-mcp/"+o.Current)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check for the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := path.Base(loc)
	if resp.StatusCode/100 != 3 || !strings.Contains(loc, "/tag/") || tag == "" {
		return "", fmt.Errorf("no release found at %s (HTTP %d)", o.BaseURL, resp.StatusCode)
	}
	return tag, nil
}

func get(ctx context.Context, client *http.Client, o Options, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sh-mcp/"+o.Current)
	// Release downloads redirect to a storage host.
	follow := *client
	follow.CheckRedirect = nil
	resp, err := follow.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, limit)
	}
	return data, nil
}

// checksumFor finds asset in a sha256sum listing ("<hash>  <name>", or
// "<hash> *<name>" in binary mode).
func checksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", asset)
}

// replace writes bin next to exe and moves it into place. Unix can rename
// over a running binary. Windows cannot overwrite one but can rename it, so
// the old binary is moved aside to exe.old first (removed by the next update).
func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".sh-mcp-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (%w); run the update as the owner of the file, e.g. with sudo", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(tmp.Name(), exe)
	}
	old := exe + ".old"
	_ = os.Remove(old) // left by a previous update
	if err := os.Rename(exe, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		_ = os.Rename(old, exe) // put the old binary back
		return err
	}
	_ = os.Remove(old) // fails while the old binary still runs
	return nil
}
