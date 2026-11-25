package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/yh2237/sh-mcp/internal/config"
)

const testToken = "0123456789abcdef0123"

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func newHTTPServer(t *testing.T, publicURL string) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	cfg.Transport = config.TransportHTTP
	cfg.AllowedDirs = []string{t.TempDir()}
	cfg.AuthToken = testToken
	if publicURL != "" {
		u, _ := url.Parse(publicURL)
		cfg.PublicURL = u
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

type httpClient struct {
	t    *testing.T
	base string
	host string
}

func (c *httpClient) do(method, path string, body io.Reader, header map[string]string) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, body)
	if c.host != "" {
		req.Host = c.host
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (c *httpClient) initialize(token string, extra map[string]string) int {
	c.t.Helper()
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	for k, v := range extra {
		h[k] = v
	}
	return c.do("POST", "/mcp", strings.NewReader(initializeBody), h).StatusCode
}

func (c *httpClient) form(path string, v url.Values) (*http.Response, map[string]any) {
	c.t.Helper()
	resp := c.do("POST", path, strings.NewReader(v.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp, out
}

func TestHTTPStaticTokenAndHost(t *testing.T) {
	ts := newHTTPServer(t, "https://mcp.example.com")
	c := &httpClient{t: t, base: ts.URL}
	cases := []struct {
		name, host, token string
		extra             map[string]string
		want              int
	}{
		{"no token", "", "", nil, 401},
		{"bad token", "", "wrong-token-wrong-token", nil, 401},
		{"ok loopback", "", testToken, nil, 200},
		{"ok public host", "mcp.example.com", testToken, nil, 200},
		{"bad host", "evil.example.net", testToken, nil, 403},
		{"cross-site browser request", "", testToken, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
	}
	for _, tc := range cases {
		c.host = tc.host
		if got := c.initialize(tc.token, tc.extra); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
	c.host = ""
	if resp := c.do("GET", "/healthz", nil, nil); resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	resp := c.do("POST", "/mcp", strings.NewReader(initializeBody), map[string]string{"Content-Type": "application/json"})
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Errorf("401 should point at resource metadata: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestOAuthFlow(t *testing.T) {
	ts := newHTTPServer(t, "https://mcp.example.com")
	c := &httpClient{t: t, base: ts.URL, host: "mcp.example.com"}

	var prm map[string]any
	json.NewDecoder(c.do("GET", "/.well-known/oauth-protected-resource/mcp", nil, nil).Body).Decode(&prm)
	if prm["resource"] != "https://mcp.example.com/mcp" {
		t.Fatalf("protected resource metadata: %v", prm)
	}
	var asm map[string]any
	json.NewDecoder(c.do("GET", "/.well-known/oauth-authorization-server", nil, nil).Body).Decode(&asm)
	if asm["registration_endpoint"] != "https://mcp.example.com/oauth/register" || asm["issuer"] != "https://mcp.example.com" {
		t.Fatalf("authorization server metadata: %v", asm)
	}

	redirect := "https://oauth-redirect.googleusercontent.com/r/test-project"
	resp := c.do("POST", "/oauth/register", strings.NewReader(`{"redirect_uris":["`+redirect+`"],"client_name":"Gemini","token_endpoint_auth_method":"client_secret_post"}`),
		map[string]string{"Content-Type": "application/json"})
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	if resp.StatusCode != 201 || reg["client_id"] == nil || reg["client_secret"] == nil {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	clientID, secret := reg["client_id"].(string), reg["client_secret"].(string)
	bad := c.do("POST", "/oauth/register", strings.NewReader(`{"redirect_uris":["javascript:alert(1)"]}`), map[string]string{"Content-Type": "application/json"})
	if bad.StatusCode != 400 {
		t.Errorf("javascript: redirect accepted: %d", bad.StatusCode)
	}

	verifier := "test-verifier-0123456789-0123456789-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"xyz"}, "scope": {"mcp"}, "resource": {"https://mcp.example.com/mcp"},
	}
	page := c.do("GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	body, _ := io.ReadAll(page.Body)
	m := regexp.MustCompile(`name="request" value="([^"]+)"`).FindSubmatch(body)
	if page.StatusCode != 200 || m == nil || !strings.Contains(string(body), "Gemini") {
		t.Fatalf("consent page: %d %s", page.StatusCode, body)
	}
	request := string(m[1])

	noPKCE := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"s"}}
	if loc := c.do("GET", "/oauth/authorize?"+noPKCE.Encode(), nil, nil).Header.Get("Location"); !strings.Contains(loc, "error=invalid_request") {
		t.Errorf("missing PKCE should redirect with invalid_request: %q", loc)
	}

	if r, _ := c.form("/oauth/authorize", url.Values{"request": {request}, "password": {"wrong"}, "action": {"approve"}}); r.StatusCode != 401 {
		t.Errorf("wrong password: %d", r.StatusCode)
	}
	r, _ := c.form("/oauth/authorize", url.Values{"request": {request}, "password": {testToken}, "action": {"approve"}})
	loc, _ := url.Parse(r.Header.Get("Location"))
	if r.StatusCode != 302 || loc == nil || !strings.HasPrefix(loc.String(), redirect) || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("approve: %d %v", r.StatusCode, loc)
	}
	code := loc.Query().Get("code")

	tokenReq := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "client_secret": {secret}, "code_verifier": {"not-the-verifier"}}
	if r, out := c.form("/oauth/token", tokenReq); r.StatusCode != 400 || out["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", r.StatusCode, out)
	}
	tokenReq.Set("code_verifier", verifier)
	r, tok := c.form("/oauth/token", tokenReq)
	if r.StatusCode != 200 || tok["access_token"] == nil || tok["refresh_token"] == nil {
		t.Fatalf("token: %d %v", r.StatusCode, tok)
	}
	if r, _ := c.form("/oauth/token", tokenReq); r.StatusCode != 400 {
		t.Errorf("code reuse: %d", r.StatusCode)
	}
	tokenReq.Set("client_secret", "wrong")
	if r, _ := c.form("/oauth/token", tokenReq); r.StatusCode != 401 {
		t.Errorf("wrong client secret: %d", r.StatusCode)
	}

	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if got := c.initialize(access, nil); got != 200 {
		t.Errorf("MCP with OAuth access token: %d", got)
	}

	refreshReq := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}, "client_secret": {secret}}
	r, tok2 := c.form("/oauth/token", refreshReq)
	if r.StatusCode != 200 || tok2["refresh_token"] != refresh || tok2["access_token"] == access {
		t.Errorf("refresh must keep the refresh token and issue a new access token: %d %v", r.StatusCode, tok2)
	}

	c.form("/oauth/revoke", url.Values{"token": {refresh}, "client_id": {clientID}, "client_secret": {secret}})
	if r, _ := c.form("/oauth/token", refreshReq); r.StatusCode != 400 {
		t.Errorf("revoked refresh token still works: %d", r.StatusCode)
	}

	for i := 0; i < 4; i++ {
		c.form("/oauth/authorize", url.Values{"request": {request}, "password": {"wrong"}, "action": {"approve"}})
	}
	if r, _ := c.form("/oauth/authorize", url.Values{"request": {request}, "password": {testToken}, "action": {"approve"}}); r.StatusCode != 429 {
		t.Errorf("rate limit after 5 failures: %d", r.StatusCode)
	}
}
