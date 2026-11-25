// Package oauth is a minimal OAuth 2.1 authorization server for a single
// owner, so remote MCP clients that only speak OAuth (such as Gemini) can
// connect. The owner approves a client by entering the server's auth token
// on the consent page.
//
// It keeps no state on disk: client registrations, authorization codes and
// tokens are HMAC-signed with a key derived from the auth token, so they
// survive restarts, and changing the auth token revokes everything. Only
// single-use codes and explicit revocations are tracked in memory.
package oauth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	Scope = "mcp"

	codeTTL    = 2 * time.Minute
	requestTTL = 10 * time.Minute
	accessTTL  = time.Hour

	maxFailures   = 5
	failureWindow = time.Minute

	kindClient  = "client"
	kindRequest = "request"
	kindCode    = "code"
	kindAccess  = "access"
	kindRefresh = "refresh"

	authNone   = "none"
	authBasic  = "client_secret_basic"
	authPost   = "client_secret_post"
	pathPrefix = "/oauth"
)

type Server struct {
	issuer   string
	resource string
	access   string
	owner    [32]byte
	key      []byte
	now      func() time.Time

	mu        sync.Mutex
	usedCodes map[string]time.Time
	revoked   map[string]time.Time
	failures  []time.Time
}

// New returns an authorization server for the MCP endpoint at
// publicURL+mcpPath. ownerSecret is both the consent password and the root of
// the signing key. access describes what an approved client may do, for the
// consent page.
func New(publicURL *url.URL, mcpPath, ownerSecret, access string) (*Server, error) {
	issuer := strings.TrimRight(publicURL.String(), "/")
	// The issuer is part of the key so that two servers sharing an auth token
	// (say a read-only and a full one) do not accept each other's tokens.
	key, err := hkdf.Key(sha256.New, []byte(ownerSecret), []byte("sh-mcp oauth"), "token signing v2\x00"+issuer, 32)
	if err != nil {
		return nil, err
	}
	return &Server{
		issuer:    issuer,
		resource:  issuer + mcpPath,
		access:    access,
		owner:     sha256.Sum256([]byte(ownerSecret)),
		key:       key,
		now:       time.Now,
		usedCodes: map[string]time.Time{},
		revoked:   map[string]time.Time{},
	}, nil
}

func (s *Server) Resource() string { return s.resource }

// ResourceMetadataURL is the RFC 9728 metadata location advertised in
// WWW-Authenticate challenges.
func (s *Server) ResourceMetadataURL() string {
	u, _ := url.Parse(s.resource)
	return s.issuer + "/.well-known/oauth-protected-resource" + u.Path
}

func (s *Server) Register(mux *http.ServeMux, csrf *http.CrossOriginProtection) {
	u, _ := url.Parse(s.resource)
	prm := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.resource,
		AuthorizationServers:   []string{s.issuer},
		ScopesSupported:        []string{Scope},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "sh-mcp",
	})
	mux.Handle("/.well-known/oauth-protected-resource", prm)
	mux.Handle("/.well-known/oauth-protected-resource"+u.Path, prm)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST "+pathPrefix+"/register", s.register)
	mux.HandleFunc("GET "+pathPrefix+"/authorize", s.authorizeForm)
	mux.Handle("POST "+pathPrefix+"/authorize", csrf.Handler(http.HandlerFunc(s.authorizeSubmit)))
	mux.HandleFunc("POST "+pathPrefix+"/token", s.token)
	mux.HandleFunc("POST "+pathPrefix+"/revoke", s.revoke)
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	methods := []string{authNone, authBasic, authPost}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                     s.issuer,
		"authorization_endpoint":                     s.issuer + pathPrefix + "/authorize",
		"token_endpoint":                             s.issuer + pathPrefix + "/token",
		"registration_endpoint":                      s.issuer + pathPrefix + "/register",
		"revocation_endpoint":                        s.issuer + pathPrefix + "/revoke",
		"response_types_supported":                   []string{"code"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      methods,
		"revocation_endpoint_auth_methods_supported": methods,
		"scopes_supported":                           []string{Scope},
	})
}

// claims is the payload of every signed value; Kind keeps one kind from
// being accepted as another.
type claims struct {
	Kind       string   `json:"k"`
	ID         string   `json:"id,omitempty"`
	Client     string   `json:"c,omitempty"`
	Name       string   `json:"n,omitempty"`
	Redirects  []string `json:"rs,omitempty"`
	AuthMethod string   `json:"am,omitempty"`
	Redirect   string   `json:"r,omitempty"`
	Challenge  string   `json:"pc,omitempty"`
	State      string   `json:"st,omitempty"`
	Issued     int64    `json:"iat"`
	Expires    int64    `json:"exp,omitempty"`
}

var b64 = base64.RawURLEncoding

func (s *Server) sign(c claims) string {
	c.Issued = s.now().Unix()
	if c.ID == "" {
		c.ID = randomID()
	}
	payload, _ := json.Marshal(c)
	p := b64.EncodeToString(payload)
	return p + "." + b64.EncodeToString(s.mac(p))
}

func (s *Server) mac(data string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

var errInvalid = errors.New("invalid or expired")

func (s *Server) verify(token, kind string) (*claims, error) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errInvalid
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(p)) {
		return nil, errInvalid
	}
	payload, err := b64.DecodeString(p)
	if err != nil {
		return nil, errInvalid
	}
	var c claims
	if json.Unmarshal(payload, &c) != nil || c.Kind != kind {
		return nil, errInvalid
	}
	if c.Expires != 0 && s.now().Unix() >= c.Expires {
		return nil, errInvalid
	}
	return &c, nil
}

func (s *Server) clientSecret(clientID string) string {
	return b64.EncodeToString(s.mac("client-secret:" + clientID))
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return b64.EncodeToString(b[:])
}

func (s *Server) VerifyAccessToken(token string) (*auth.TokenInfo, error) {
	c, err := s.verify(token, kindAccess)
	if err != nil || s.isRevoked(c.ID) {
		return nil, auth.ErrInvalidToken
	}
	client, err := s.verify(c.Client, kindClient)
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		Scopes:     []string{Scope},
		Expiration: time.Unix(c.Expires, 0),
		UserID:     fmt.Sprintf("oauth:%s#%s", client.Name, client.ID[:8]),
	}, nil
}

func (s *Server) isRevoked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.revoked[id]
	return ok
}

// --- Dynamic client registration (RFC 7591) ---

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be JSON client metadata")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 10 redirect_uris are required")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := checkRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	if req.AuthMethod == "" {
		req.AuthMethod = authBasic
	}
	if !slices.Contains([]string{authNone, authBasic, authPost}, req.AuthMethod) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "unnamed client"
	}
	name = truncate(name, 80)
	clientID := s.sign(claims{Kind: kindClient, Name: name, Redirects: req.RedirectURIs, AuthMethod: req.AuthMethod})
	resp := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        s.now().Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": req.AuthMethod,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      Scope,
	}
	if req.AuthMethod != authNone {
		resp["client_secret"] = s.clientSecret(clientID)
		resp["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, resp)
}

// checkRedirectURI allows https, http on loopback (native apps) and private
// URI schemes, and rejects schemes that run code or read local files.
func checkRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Fragment != "" {
		return fmt.Errorf("invalid redirect_uri %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("invalid redirect_uri %q", raw)
		}
	case "http":
		h := u.Hostname()
		if h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return fmt.Errorf("http redirect_uri must be on localhost: %q", raw)
		}
	case "javascript", "data", "file", "vbscript", "blob", "about":
		return fmt.Errorf("redirect_uri scheme not allowed: %q", raw)
	}
	return nil
}

// --- Authorization endpoint ---

type authzError struct {
	code, desc string
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, redirect, err := s.clientAndRedirect(q.Get("client_id"), q.Get("redirect_uri"))
	if err != nil {
		// Without a trusted redirect_uri, errors must not be redirected.
		s.renderError(w, http.StatusBadRequest, err.Error())
		return
	}
	state := q.Get("state")
	if e := s.checkAuthzRequest(q); e != nil {
		redirectWith(w, r, redirect, url.Values{"error": {e.code}, "error_description": {e.desc}, "state": {state}, "iss": {s.issuer}})
		return
	}
	req := s.sign(claims{
		Kind:      kindRequest,
		Client:    q.Get("client_id"),
		Redirect:  redirect,
		Challenge: q.Get("code_challenge"),
		State:     state,
		Expires:   s.now().Add(requestTTL).Unix(),
	})
	s.renderConsent(w, http.StatusOK, client.Name, redirect, req, "")
}

func (s *Server) clientAndRedirect(clientID, redirect string) (*claims, string, error) {
	client, err := s.verify(clientID, kindClient)
	if err != nil {
		return nil, "", errors.New("unknown client_id; register the client again")
	}
	if redirect == "" {
		if len(client.Redirects) != 1 {
			return nil, "", errors.New("redirect_uri is required")
		}
		return client, client.Redirects[0], nil
	}
	if !slices.Contains(client.Redirects, redirect) {
		return nil, "", errors.New("redirect_uri is not registered for this client")
	}
	return client, redirect, nil
}

func (s *Server) checkAuthzRequest(q url.Values) *authzError {
	if q.Get("response_type") != "code" {
		return &authzError{"unsupported_response_type", "only response_type=code is supported"}
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return &authzError{"invalid_request", "PKCE with code_challenge_method=S256 is required"}
	}
	if res := q.Get("resource"); res != "" && !s.validResource(res) {
		return &authzError{"invalid_target", "unknown resource"}
	}
	return nil
}

func (s *Server) validResource(res string) bool {
	res = strings.TrimRight(res, "/")
	return res == s.resource || res == s.issuer
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, "invalid form")
		return
	}
	req, err := s.verify(r.PostForm.Get("request"), kindRequest)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, "this approval page has expired; start the connection again from the client")
		return
	}
	client, err := s.verify(req.Client, kindClient)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, "unknown client")
		return
	}
	if r.PostForm.Get("action") != "approve" {
		redirectWith(w, r, req.Redirect, url.Values{"error": {"access_denied"}, "state": {req.State}, "iss": {s.issuer}})
		return
	}
	if !s.allowAttempt() {
		s.renderConsent(w, http.StatusTooManyRequests, client.Name, req.Redirect, r.PostForm.Get("request"),
			"Too many wrong attempts. Wait a minute and try again.")
		return
	}
	got := sha256.Sum256([]byte(r.PostForm.Get("password")))
	if subtle.ConstantTimeCompare(got[:], s.owner[:]) != 1 {
		s.recordFailure()
		s.renderConsent(w, http.StatusUnauthorized, client.Name, req.Redirect, r.PostForm.Get("request"), "Wrong token.")
		return
	}
	code := s.sign(claims{
		Kind:      kindCode,
		Client:    req.Client,
		Redirect:  req.Redirect,
		Challenge: req.Challenge,
		Expires:   s.now().Add(codeTTL).Unix(),
	})
	redirectWith(w, r, req.Redirect, url.Values{"code": {code}, "state": {req.State}, "iss": {s.issuer}})
}

func (s *Server) allowAttempt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-failureWindow)
	s.failures = slices.DeleteFunc(s.failures, func(t time.Time) bool { return t.Before(cutoff) })
	return len(s.failures) < maxFailures
}

func (s *Server) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, s.now())
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirect string, params url.Values) {
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, v := range params {
		if len(v) > 0 && v[0] != "" {
			q.Set(k, v[0])
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// --- Token endpoint ---

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	clientID, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	if res := r.PostForm.Get("resource"); res != "" && !s.validResource(res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "unknown resource")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code, err := s.verify(r.PostForm.Get("code"), kindCode)
		if err != nil || code.Client != clientID {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired code")
			return
		}
		if ru := r.PostForm.Get("redirect_uri"); ru != "" && ru != code.Redirect {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if subtle.ConstantTimeCompare([]byte(b64.EncodeToString(sum[:])), []byte(code.Challenge)) != 1 {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match")
			return
		}
		if !s.useCode(code) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code was already used")
			return
		}
		refresh := s.sign(claims{Kind: kindRefresh, Client: clientID})
		s.issueAccess(w, clientID, refresh)
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		c, err := s.verify(rt, kindRefresh)
		if err != nil || c.Client != clientID || s.isRevoked(c.ID) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh_token")
			return
		}
		// The same refresh token is returned: Gemini requires refresh tokens
		// not to rotate.
		s.issueAccess(w, clientID, rt)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) issueAccess(w http.ResponseWriter, clientID, refresh string) {
	access := s.sign(claims{Kind: kindAccess, Client: clientID, Expires: s.now().Add(accessTTL).Unix()})
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         Scope,
	})
}

func (s *Server) useCode(c *claims) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, exp := range s.usedCodes {
		if now.After(exp) {
			delete(s.usedCodes, id)
		}
	}
	if _, used := s.usedCodes[c.ID]; used {
		return false
	}
	s.usedCodes[c.ID] = time.Unix(c.Expires, 0)
	return true
}

// authenticateClient checks the client_id and, for confidential clients, the
// secret from HTTP Basic auth or the form body.
func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) (string, bool) {
	clientID, secret, basic := r.BasicAuth()
	if basic {
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
	} else {
		clientID, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	client, err := s.verify(clientID, kindClient)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return "", false
	}
	if client.AuthMethod != authNone {
		want := s.clientSecret(clientID)
		if subtle.ConstantTimeCompare([]byte(secret), []byte(want)) != 1 {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
			return "", false
		}
	}
	return clientID, true
}

// --- Revocation (RFC 7009) ---

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	clientID, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	tok := r.PostForm.Get("token")
	for _, kind := range []string{kindRefresh, kindAccess} {
		if c, err := s.verify(tok, kind); err == nil && c.Client == clientID {
			s.mu.Lock()
			s.revoked[c.ID] = s.now()
			s.mu.Unlock()
		}
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
