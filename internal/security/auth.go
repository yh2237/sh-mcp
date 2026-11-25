package security

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// BearerVerifier accepts the static auth token or, if oauth is non-nil, an
// OAuth access token. The static token is hashed first so the comparison time
// does not depend on its length either.
func BearerVerifier(static string, oauth func(string) (*auth.TokenInfo, error)) auth.TokenVerifier {
	want := sha256.Sum256([]byte(static))
	return func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		h := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(h[:], want[:]) == 1 {
			return &auth.TokenInfo{UserID: "static-token"}, nil
		}
		if oauth != nil {
			return oauth(got)
		}
		return nil, auth.ErrInvalidToken
	}
}

// With resourceMetadataURL set, the 401 response points OAuth clients at the
// protected resource metadata (RFC 9728).
func RequireBearer(verifier auth.TokenVerifier, resourceMetadataURL string, next http.Handler) http.Handler {
	return auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL:    resourceMetadataURL,
		AllowMissingExpiration: true,
	})(next)
}

// CheckHost rejects requests whose Host header is neither loopback nor in
// allowed. It replaces the SDK's built-in DNS-rebinding protection, which only
// accepts loopback Host values and so breaks tunnels and reverse proxies that
// forward the public host name.
func CheckHost(allowed []string, next http.Handler) http.Handler {
	set := map[string]bool{}
	for _, h := range allowed {
		set[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		name := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			name = h
		}
		if !set[host] && !set[name] && !isLoopbackName(name) {
			http.Error(w, "Forbidden: invalid Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackName(name string) bool {
	name = strings.Trim(name, "[]")
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
