package oauth

import (
	"net/url"
	"testing"
	"time"
)

func newTestServer(t *testing.T, rawURL, secret string, now *time.Time) *Server {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(u, "/mcp", secret, "read files")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	return s
}

func TestTokenExpiryAndKindSeparation(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	s := newTestServer(t, "https://mcp.example.com", "0123456789abcdef0123", &now)

	clientID := s.sign(claims{
		Kind:       kindClient,
		Name:       "TestClient",
		Redirects:  []string{"https://client.example/cb"},
		AuthMethod: authNone,
	})
	reqTok := s.sign(claims{Kind: kindRequest, Client: clientID, Expires: now.Add(requestTTL).Unix()})
	codeTok := s.sign(claims{Kind: kindCode, Client: clientID, Expires: now.Add(codeTTL).Unix()})
	accessTok := s.sign(claims{Kind: kindAccess, Client: clientID, Expires: now.Add(accessTTL).Unix()})
	refreshTok := s.sign(claims{Kind: kindRefresh, Client: clientID})

	// A token of one kind must not verify as another.
	if _, err := s.verify(refreshTok, kindAccess); err == nil {
		t.Error("refresh token verified as access token")
	}
	if _, err := s.VerifyAccessToken(accessTok); err != nil {
		t.Fatalf("fresh access token rejected: %v", err)
	}

	now = now.Add(codeTTL)
	if _, err := s.verify(codeTok, kindCode); err == nil {
		t.Error("expired authorization code was accepted")
	}
	if _, err := s.verify(reqTok, kindRequest); err != nil {
		t.Errorf("request token expired too early: %v", err)
	}

	now = now.Add(requestTTL)
	if _, err := s.verify(reqTok, kindRequest); err == nil {
		t.Error("expired consent request was accepted")
	}

	now = now.Add(accessTTL)
	if _, err := s.VerifyAccessToken(accessTok); err == nil {
		t.Error("expired access token was accepted")
	}
	if _, err := s.verify(refreshTok, kindRefresh); err != nil {
		t.Errorf("refresh token should not expire: %v", err)
	}
}

func TestIssuerIsolation(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	secret := "shared-secret-0123456789"
	s1 := newTestServer(t, "https://read.example.com", secret, &now)
	s2 := newTestServer(t, "https://full.example.com", secret, &now)

	clientID := s1.sign(claims{Kind: kindClient, Name: "Client", AuthMethod: authNone})
	accessTok := s1.sign(claims{Kind: kindAccess, Client: clientID, Expires: now.Add(accessTTL).Unix()})

	if _, err := s1.VerifyAccessToken(accessTok); err != nil {
		t.Fatalf("token failed on its own issuer: %v", err)
	}
	if _, err := s2.VerifyAccessToken(accessTok); err == nil {
		t.Error("token from another issuer was accepted despite sharing the owner secret")
	}
}
