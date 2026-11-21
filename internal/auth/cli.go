package auth

// OAuth 2.0 for native apps (RFC 8252) with containerservice as the authorization server, so the
// command-line client cs (or any standard OAuth client) gets a bearer token: the authorization
// code grant (RFC 6749 §4.1) with PKCE S256 (RFC 7636), one public client "cs", and loopback
// redirect URIs on any port (RFC 8252 §7.3).
//
//	GET  /oauth/authorize  response_type=code client_id=cs redirect_uri=http://127.0.0.1:<port>/callback
//	                       code_challenge=… code_challenge_method=S256 state=…
//	                       Without a session the browser signs in first; then a page to allow it.
//	POST /oauth/authorize  the page's Allow/Deny: a redirect to redirect_uri with code and state.
//	POST /oauth/token      grant_type=authorization_code code redirect_uri client_id code_verifier
//	                       → {access_token, token_type: "Bearer", expires_in}
//
// The code is sealed {session, challenge, redirect_uri, client}, two minutes long, and useless
// without the verifier; the access token is a session sealed for bearer use, which the API takes
// as Authorization: Bearer. The allow page keeps a web page from silently minting codes.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	cliToken   = "containerservice_cli"
	cliCode    = "containerservice_cli_code"
	codeWindow = 2 * time.Minute
	// ClientID is the one OAuth client: cs, a public native app.
	ClientID = "cs"
)

// code is the sealed authorization code.
type code struct {
	Session     Session   `json:"s"`
	Challenge   string    `json:"c"`
	RedirectURI string    `json:"r"`
	ClientID    string    `json:"i"`
	Expires     time.Time `json:"e"`
}

var (
	challengeRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	stateRE     = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
)

// authRequest is an authorization request's parameters.
type authRequest struct {
	RedirectURI, Challenge, State string
}

// loopback reports whether u is a loopback redirect URI for cs: http, 127.0.0.1 or [::1], any
// port, path /callback, no query or fragment.
func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/callback" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback() && u.Port() != ""
}

// parseAuthorize checks an authorization request. A bad client or redirect URI is shown, never
// redirected to (RFC 6749 §4.1.2.1); other errors go back to the redirect URI.
func (a *Auth) parseAuthorize(w http.ResponseWriter, r *http.Request, v url.Values) (authRequest, bool) {
	req := authRequest{RedirectURI: v.Get("redirect_uri"), Challenge: v.Get("code_challenge"), State: v.Get("state")}
	if v.Get("client_id") != ClientID || !loopback(req.RedirectURI) {
		a.log.Info("oauth: refused authorize", "why", "client or redirect_uri", "client", v.Get("client_id"), "redirect_uri", req.RedirectURI)
		a.refuse(w, http.StatusBadRequest, "This sign-in request isn't from cs (unknown client, or a redirect that isn't http://127.0.0.1:<port>/callback).")
		return req, false
	}
	why := ""
	switch {
	case v.Get("response_type") != "code":
		why = "unsupported_response_type"
	case v.Get("code_challenge_method") != "S256" || !challengeRE.MatchString(req.Challenge):
		why = "invalid_request" // PKCE S256 is required
	case !stateRE.MatchString(req.State):
		why = "invalid_request"
	}
	if why != "" {
		a.log.Info("oauth: refused authorize", "why", why)
		a.redirectError(w, r, req, why)
		return req, false
	}
	return req, true
}

func (a *Auth) redirectError(w http.ResponseWriter, r *http.Request, req authRequest, e string) {
	q := url.Values{"error": {e}}
	if req.State != "" {
		q.Set("state", req.State)
	}
	http.Redirect(w, r, req.RedirectURI+"?"+q.Encode(), http.StatusSeeOther)
}

var allowPage = template.Must(template.New("").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in cs</title>
<style>body{font:14px system-ui,sans-serif;max-width:28rem;margin:15vh auto;padding:0 1rem;color:#222}
@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}}button{font:inherit;padding:.5rem 1rem;cursor:pointer;margin-right:.5rem}</style></head>
<body><h1>Sign in cs?</h1>
<p>The command-line client on this computer asks to act as <b>{{.User}}</b> on {{.Origin}}. Allow it only if you just ran <code>cs login</code>.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="response_type" value="code"><input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"><input type="hidden" name="code_challenge" value="{{.Challenge}}">
<input type="hidden" name="code_challenge_method" value="S256"><input type="hidden" name="state" value="{{.State}}">
<button type="submit" name="decision" value="allow">Allow</button><button type="submit" name="decision" value="deny">Deny</button>
</form></body></html>`))

// authorize (GET) shows the allow page, signing the browser in first.
func (a *Auth) authorize(w http.ResponseWriter, r *http.Request) {
	req, ok := a.parseAuthorize(w, r, r.URL.Query())
	if !ok {
		return
	}
	s, signedIn := a.cookieSession(r)
	if !signedIn {
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape("/oauth/authorize?"+r.URL.RawQuery), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The form posts here and the answer redirects to the loopback listener: both form targets.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' http://127.0.0.1:* http://[::1]:*; frame-ancestors 'none'; base-uri 'none'")
	allowPage.Execute(w, map[string]string{"User": s.Username, "Origin": a.cfg.Origin, "ClientID": ClientID,
		"RedirectURI": req.RedirectURI, "Challenge": req.Challenge, "State": req.State})
}

// decide (POST from the allow page) redirects to the client with a code, or access_denied.
func (a *Auth) decide(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		a.refuse(w, http.StatusBadRequest, "The sign-in form couldn't be read; run cs login again.")
		return
	}
	req, ok := a.parseAuthorize(w, r, r.PostForm)
	if !ok {
		return
	}
	// A cross-site POST gets no Lax cookie; and an Origin, when sent, must be ours.
	if o := r.Header.Get("Origin"); o != "" && o != a.cfg.Origin {
		a.log.Info("oauth: refused allow", "why", "origin", "origin", o)
		a.refuse(w, http.StatusForbidden, "This sign-in can only be allowed from "+a.cfg.Origin+".")
		return
	}
	s, signedIn := a.cookieSession(r)
	if !signedIn {
		a.log.Info("oauth: refused allow", "why", "no session cookie")
		a.refuse(w, http.StatusUnauthorized, "This browser isn't signed in any more; run cs login again.")
		return
	}
	if r.PostForm.Get("decision") != "allow" {
		a.log.Info("oauth: denied", "user", s.Username)
		a.redirectError(w, r, req, "access_denied")
		return
	}
	value, err := a.seal.seal(cliCode, code{Session: s, Challenge: req.Challenge, RedirectURI: req.RedirectURI, ClientID: ClientID, Expires: time.Now().Add(codeWindow)})
	if err != nil {
		a.refuse(w, http.StatusInternalServerError, "The code couldn't be made.")
		return
	}
	a.log.Info("oauth: code issued", "user", s.Username)
	q := url.Values{"code": {value}, "state": {req.State}}
	http.Redirect(w, r, req.RedirectURI+"?"+q.Encode(), http.StatusSeeOther)
}

// token exchanges a code for an access token (RFC 6749 §4.1.3, RFC 7636 §4.5).
func (a *Auth) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	fail := func(e, description string) {
		a.log.Info("oauth: refused token", "error", e, "why", description)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": e, "error_description": description})
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		fail("invalid_request", "the body is application/x-www-form-urlencoded")
		return
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" {
		fail("unsupported_grant_type", "only authorization_code")
		return
	}
	if f.Get("client_id") != ClientID {
		fail("invalid_client", "the client is cs")
		return
	}
	var c code
	if a.seal.open(cliCode, f.Get("code"), &c) != nil || !time.Now().Before(c.Expires) {
		fail("invalid_grant", "the code is not valid or has expired")
		return
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	switch {
	case c.ClientID != ClientID || c.RedirectURI != f.Get("redirect_uri"):
		fail("invalid_grant", "redirect_uri or client differs from the authorization request")
		return
	case subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.Challenge)) != 1:
		fail("invalid_grant", "code_verifier doesn't match the code_challenge")
		return
	}
	s := c.Session
	s.Expires = time.Now().Add(a.cfg.Lifetime).Truncate(time.Second)
	token, err := a.seal.seal(cliToken, s)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "server_error"})
		return
	}
	a.log.Info("oauth: token issued", "user", s.Username)
	json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer",
		"expires_in": int(time.Until(s.Expires).Seconds()), "username": s.Username})
}

// bearer is the request's Authorization: Bearer token, if any.
func bearer(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(token), ok
}

// HasBearer reports whether the request authenticates with a bearer token (not the cookie): it
// needs no CSRF guard, since browsers don't attach it on their own.
func HasBearer(r *http.Request) bool {
	_, ok := bearer(r)
	return ok
}
