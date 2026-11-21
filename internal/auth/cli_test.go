package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func oauthAuth(t *testing.T) (*Auth, http.Handler, string) {
	a := testAuth(t)
	a.cfg = Config{Origin: "https://cs.example", Lifetime: time.Hour}
	a.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	a.Routes(mux)
	cookie, _ := a.seal.seal(sessionCookie, Session{Owner: strings.Repeat("a", 40), Username: "me", Expires: time.Now().Add(time.Hour)})
	return a, mux, cookie
}

const redirect = "http://127.0.0.1:64027/callback"

func authorizeQuery(verifier string) url.Values {
	sum := sha256.Sum256([]byte(verifier))
	return url.Values{"response_type": {"code"}, "client_id": {"cs"}, "redirect_uri": {redirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {"st-123"}}
}

// allowForm is what the allow page's form submits, read from the page.
func allowForm(t *testing.T, h http.Handler, cookie string, q url.Values) url.Values {
	r := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("allow page: %d %s", w.Code, w.Body)
	}
	if p := w.Header().Get("Referrer-Policy"); p != "same-origin" {
		t.Errorf("the allow page's Referrer-Policy is %q: with no-referrer browsers post Origin: null", p)
	}
	form := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="(\w+)" value="([^"]*)">`).FindAllStringSubmatch(w.Body.String(), -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	return form
}

func post(h http.Handler, path string, form url.Values, cookie, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOAuthCodeFlow(t *testing.T) {
	a, h, cookie := oauthAuth(t)
	verifier := strings.Repeat("v", 50)
	q := authorizeQuery(verifier)

	// Not signed in: the usual sign-in first, coming back here.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/auth/login?next=%2Foauth%2Fauthorize%3F") {
		t.Fatalf("unsigned: %d %s", w.Code, w.Header().Get("Location"))
	}
	form := allowForm(t, h, cookie, q)
	if form.Encode() != q.Encode() {
		t.Fatalf("the page posts %q, not the request %q", form.Encode(), q.Encode())
	}
	form.Set("decision", "allow")
	if w := post(h, "/oauth/authorize", form, cookie, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site allow: %d", w.Code)
	}
	if w := post(h, "/oauth/authorize", form, "", "https://cs.example"); w.Code != http.StatusUnauthorized {
		t.Errorf("allow without a session: %d", w.Code)
	}
	w = post(h, "/oauth/authorize", form, cookie, "https://cs.example")
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Scheme+"://"+loc.Host+loc.Path != redirect || loc.Query().Get("state") != "st-123" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect: %d %s", w.Code, loc)
	}
	tokenForm := func(change map[string]string) url.Values {
		f := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect}, "client_id": {"cs"}, "code_verifier": {verifier}}
		for k, v := range change {
			f.Set(k, v)
		}
		return f
	}
	for what, change := range map[string]map[string]string{
		"wrong verifier": {"code_verifier": strings.Repeat("x", 50)},
		"other redirect": {"redirect_uri": "http://127.0.0.1:1/callback"},
		"other client":   {"client_id": "evil"},
		"other grant":    {"grant_type": "password"},
		"forged code":    {"code": "nope"},
	} {
		w := post(h, "/oauth/token", tokenForm(change), "", "")
		var e struct{ Error string }
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
	}
	w = post(h, "/oauth/token", tokenForm(nil), "", "")
	var got struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.TokenType != "Bearer" || got.AccessToken == "" || got.ExpiresIn < 3500 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	// The access token is a session as a bearer, and only as one.
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.Header.Set("Authorization", "Bearer "+got.AccessToken)
	if s, ok := a.Session(r); !ok || s.Username != "me" || !HasBearer(r) {
		t.Errorf("bearer session: %v %+v", ok, s)
	}
	if _, ok := a.Session(withCookie(sessionCookie, got.AccessToken)); ok {
		t.Error("the access token works as a cookie")
	}
	r = httptest.NewRequest("GET", "/api/session", nil)
	r.Header.Set("Authorization", "Bearer "+cookie)
	if _, ok := a.Session(r); ok {
		t.Error("the cookie works as a bearer token")
	}
}

func TestOAuthDenyAndBadRequests(t *testing.T) {
	_, h, cookie := oauthAuth(t)
	q := authorizeQuery(strings.Repeat("v", 50))
	form := allowForm(t, h, cookie, q)
	form.Set("decision", "deny")
	w := post(h, "/oauth/authorize", form, cookie, "https://cs.example")
	if loc, _ := url.Parse(w.Header().Get("Location")); loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "st-123" {
		t.Errorf("deny: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Never redirected to: a foreign client or a redirect that isn't loopback /callback.
	for _, bad := range []map[string]string{
		{"client_id": "evil"},
		{"redirect_uri": "https://evil.example/callback"},
		{"redirect_uri": "http://localhost:64027/callback"},
		{"redirect_uri": "http://127.0.0.1:64027/other"},
		{"redirect_uri": "http://127.0.0.1/callback"},
		{"redirect_uri": "http://127.0.0.1:64027/callback?x=1"},
	} {
		v := authorizeQuery("x")
		for k, val := range bad {
			v.Set(k, val)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/oauth/authorize?"+v.Encode(), nil))
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Errorf("%v: %d %s", bad, w.Code, w.Header().Get("Location"))
		}
	}
	// Redirected with an error: no PKCE, or plain.
	for _, bad := range []map[string]string{{"code_challenge_method": "plain"}, {"code_challenge": ""}, {"response_type": "token"}} {
		v := authorizeQuery("x")
		for k, val := range bad {
			v.Set(k, val)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/oauth/authorize?"+v.Encode(), nil))
		if loc, _ := url.Parse(w.Header().Get("Location")); loc == nil || loc.Query().Get("error") == "" {
			t.Errorf("%v: %d %s", bad, w.Code, w.Header().Get("Location"))
		}
	}
	// [::1] is loopback too.
	if !loopback("http://[::1]:5000/callback") {
		t.Error("[::1] refused")
	}
}
