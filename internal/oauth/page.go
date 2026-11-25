package oauth

import (
	"html/template"
	"net/http"
	"net/url"
)

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>sh-mcp: authorize {{.Client}}</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 32rem; margin: 3rem auto; padding: 0 1rem; color: #222; background: #fff; }
  .box { border: 1px solid #ccc; border-radius: 8px; padding: 1.25rem 1.5rem; }
  h1 { font-size: 1.25rem; margin-top: 0; }
  code { background: #f2f2f2; padding: 0 .25rem; border-radius: 3px; word-break: break-all; }
  .warn { background: #fff4e5; border-left: 4px solid #f0a020; padding: .5rem .75rem; }
  .err { color: #b00020; font-weight: 600; }
  input[type=password] { width: 100%; padding: .5rem; font-size: 1rem; box-sizing: border-box; }
  .buttons { display: flex; gap: .5rem; margin-top: 1rem; }
  button { padding: .5rem 1rem; font-size: 1rem; cursor: pointer; }
  @media (prefers-color-scheme: dark) {
    body { color: #eee; background: #181818; } .box { border-color: #444; }
    code { background: #2a2a2a; } .warn { background: #3a2e12; }
  }
</style>
</head>
<body>
<div class="box">
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Request}}
<h1>Allow “{{.Client}}” to use sh-mcp?</h1>
<p>It will be sent back to <code>{{.RedirectHost}}</code>.</p>
<p class="warn">The client gets the same access as this server allows ({{.Access}}) on <code>{{.Issuer}}</code>. Only approve a connection you started yourself.</p>
<form method="post" autocomplete="off">
  <input type="hidden" name="request" value="{{.Request}}">
  <label for="password">Server auth token (<code>SH_MCP_AUTH_TOKEN</code>)</label>
  <input type="password" id="password" name="password" autofocus>
  <div class="buttons">
    <button type="submit" name="action" value="approve">Approve</button>
    <button type="submit" name="action" value="deny">Deny</button>
  </div>
</form>
{{end}}
</div>
</body>
</html>`))

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
