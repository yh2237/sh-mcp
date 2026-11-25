package oauth

import (
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
)

// consent.html is embedded, so the binary stays self-contained.
//
//go:embed consent.html
var consentHTML string

var consentPage = template.Must(template.New("consent").Parse(consentHTML))

type pageData struct {
	Client, RedirectHost, Issuer, Access, Request, Error string
}

func (s *Server) renderConsent(w http.ResponseWriter, status int, client, redirect, request, errMsg string) {
	host := redirect
	if u, err := url.Parse(redirect); err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	s.render(w, status, pageData{Client: client, RedirectHost: host, Issuer: s.issuer, Access: s.access, Request: request, Error: errMsg})
}

func (s *Server) renderError(w http.ResponseWriter, status int, msg string) {
	s.render(w, status, pageData{Error: msg})
}

func (s *Server) render(w http.ResponseWriter, status int, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = consentPage.Execute(w, d)
}
