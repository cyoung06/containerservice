package auth

// Signing in the command-line client (cmd/cs), OAuth-style with containerservice as the
// authorization server, for a native app on a loopback redirect (RFC 8252, with PKCE, RFC 7636):
//
//  1. cs opens GET /cli/login?port=P&challenge=C&state=S in the browser (C = base64url(SHA-256(V))
//     of a verifier V only cs knows). Without a session, the browser signs in as usual first.
//  2. The page asks to confirm (a form posting port, challenge and state); the POST seals a code {session, C, expiry} and redirects to
//     http://127.0.0.1:P/callback?code=…&state=S.
//  3. cs POSTs {code, verifier: V} to /cli/token and gets a bearer token: a session like the
//     cookie's, sealed for "cli", that the API takes as Authorization: Bearer.
//
// The code is useless without V, so another program catching the redirect can't use it; the
// confirmation keeps a web page from silently minting codes for a port of its choosing.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	cliToken   = "containerservice_cli"
	cliCode    = "containerservice_cli_code"
	codeWindow = 2 * time.Minute
)

// code is the sealed authorization code.
type code struct {
	Session   Session   `json:"s"`
	Challenge string    `json:"c"`
	Expires   time.Time `json:"e"`
}

var (
	challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	stateRE     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
)

// cliParams reads and checks port, challenge and state.
func cliParams(v url.Values) (int, string, string, bool) {
	port, err := strconv.Atoi(v.Get("port"))
	ok := err == nil && port >= 1024 && port <= 65535 && challengeRE.MatchString(v.Get("challenge")) && stateRE.MatchString(v.Get("state"))
	return port, v.Get("challenge"), v.Get("state"), ok
}

var confirmPage = template.Must(template.New("").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in cs</title>
<style>body{font:14px system-ui,sans-serif;max-width:28rem;margin:15vh auto;padding:0 1rem;color:#222}
@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}}button{font:inherit;padding:.5rem 1rem;cursor:pointer}</style></head>
<body><h1>Sign in cs?</h1>
<p>The command-line client on this computer asks to act as <b>{{.User}}</b> on {{.Origin}}. Continue only if you just ran <code>cs login</code>.</p>
<form method="post" action="/cli/login"><input type="hidden" name="port" value="{{.Port}}"><input type="hidden" name="challenge" value="{{.Challenge}}"><input type="hidden" name="state" value="{{.State}}"><button type="submit">Sign in cs</button></form>
</body></html>`))

func (a *Auth) cliLogin(w http.ResponseWriter, r *http.Request) {
	port, challenge, state, ok := cliParams(r.URL.Query())
	if !ok {
		a.refuse(w, http.StatusBadRequest, "This isn't a sign-in from cs (bad port, challenge or state).")
		return
	}
	s, signedIn := a.cookieSession(r)
	if !signedIn {
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape("/cli/login?"+r.URL.RawQuery), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The form posts here, and the answer redirects to the loopback listener: both form targets.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' http://127.0.0.1:*; frame-ancestors 'none'; base-uri 'none'")
	confirmPage.Execute(w, map[string]any{"User": s.Username, "Origin": a.cfg.Origin, "Port": port, "Challenge": challenge, "State": state})
}

func (a *Auth) cliConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	r.ParseForm()
	port, challenge, state, ok := cliParams(r.PostForm)
	s, signedIn := a.cookieSession(r)
	// A cross-site POST: browsers send no Lax cookie with it, and Origin, when sent, must be ours.
	if o := r.Header.Get("Origin"); o != "" && o != a.cfg.Origin {
		ok = false
	}
	if !ok || !signedIn {
		a.refuse(w, http.StatusBadRequest, "This sign-in from cs can't be confirmed; run cs login again.")
		return
	}
	value, err := a.seal.seal(cliCode, code{Session: s, Challenge: challenge, Expires: time.Now().Add(codeWindow)})
	if err != nil {
		a.refuse(w, http.StatusInternalServerError, "The code couldn't be made.")
		return
	}
	a.log.Info("cs sign-in confirmed", "user", s.Username)
	http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%d/callback?code=%s&state=%s", port, url.QueryEscape(value), url.QueryEscape(state)), http.StatusSeeOther)
}

func (a *Auth) cliExchange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	fail := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"detail": msg})
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b) != nil {
		fail(http.StatusBadRequest, "Send {code, verifier}.")
		return
	}
	var c code
	sum := sha256.Sum256([]byte(b.Verifier))
	if a.seal.open(cliCode, b.Code, &c) != nil || !time.Now().Before(c.Expires) ||
		subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.Challenge)) != 1 {
		fail(http.StatusBadRequest, "The code is wrong, expired or not for this verifier; run cs login again.")
		return
	}
	s := c.Session
	s.Expires = time.Now().Add(a.cfg.Lifetime).Truncate(time.Second)
	token, err := a.seal.seal(cliToken, s)
	if err != nil {
		fail(http.StatusInternalServerError, "The token couldn't be made.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"token": token, "username": s.Username, "expires": s.Expires.UTC()})
}

// bearer is the request's Authorization: Bearer token, if any.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(h, "Bearer ")
	return strings.TrimSpace(token), ok
}

// HasBearer reports whether the request authenticates with a bearer token (not the cookie): it
// needs no CSRF guard, since browsers don't attach it on their own.
func HasBearer(r *http.Request) bool {
	_, ok := bearer(r)
	return ok
}
