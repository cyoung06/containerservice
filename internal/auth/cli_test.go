package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func cliAuth(t *testing.T) (*Auth, http.Handler, string) {
	a := testAuth(t)
	a.cfg = Config{Origin: "https://cs.example", Lifetime: time.Hour}
	a.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	a.Routes(mux)
	cookie, _ := a.seal.seal(sessionCookie, Session{Owner: strings.Repeat("a", 40), Username: "me", Expires: time.Now().Add(time.Hour)})
	return a, mux, cookie
}

func TestCLISignIn(t *testing.T) {
	a, h, cookie := cliAuth(t)
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"port": {"54321"}, "challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "state": {"state-1234567890ab"}}.Encode()

	// Not signed in: the usual sign-in first, coming back here.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/cli/login?"+q, nil))
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/auth/login?next=%2Fcli%2Flogin%3F") {
		t.Fatalf("unsigned: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Signed in: a page to confirm, not a code.
	r := httptest.NewRequest("GET", "/cli/login?"+q, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `method="post"`) {
		t.Fatalf("confirm page: %d", w.Code)
	}
	// A POST from another site is refused.
	r = httptest.NewRequest("POST", "/cli/login?"+q, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("cross-site confirm: %d", w.Code)
	}
	// Confirmed: a code to the loopback listener.
	r = httptest.NewRequest("POST", "/cli/login?"+q, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	r.Header.Set("Origin", "https://cs.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Host != "127.0.0.1:54321" || loc.Path != "/callback" || loc.Query().Get("state") != "state-1234567890ab" {
		t.Fatalf("redirect: %d %s", w.Code, loc)
	}
	exchange := func(code, verifier string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"code": code, "verifier": verifier})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/cli/token", strings.NewReader(string(body))))
		return w
	}
	if w := exchange(loc.Query().Get("code"), strings.Repeat("x", 50)); w.Code != http.StatusBadRequest {
		t.Errorf("wrong verifier: %d", w.Code)
	}
	w = exchange(loc.Query().Get("code"), verifier)
	var got struct{ Token, Username string }
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.Username != "me" || got.Token == "" {
		t.Fatalf("exchange: %d %s", w.Code, w.Body)
	}
	// The token is a session as a bearer, and only as one: not as the cookie, and the cookie's
	// value isn't a bearer token.
	r = httptest.NewRequest("GET", "/api/session", nil)
	r.Header.Set("Authorization", "Bearer "+got.Token)
	if s, ok := a.Session(r); !ok || s.Username != "me" || !HasBearer(r) {
		t.Errorf("bearer session: %v %+v", ok, s)
	}
	if _, ok := a.Session(withCookie(sessionCookie, got.Token)); ok {
		t.Error("the cs token works as a cookie")
	}
	r = httptest.NewRequest("GET", "/api/session", nil)
	r.Header.Set("Authorization", "Bearer "+cookie)
	if _, ok := a.Session(r); ok {
		t.Error("the cookie works as a bearer token")
	}
}

func TestCLIParams(t *testing.T) {
	good := url.Values{"port": {"54321"}, "challenge": {strings.Repeat("c", 43)}, "state": {strings.Repeat("s", 16)}}
	if _, _, _, ok := cliParams(good); !ok {
		t.Fatal("good params refused")
	}
	for key, bad := range map[string]string{"port": "80", "challenge": "short", "state": "has space and more"} {
		v := url.Values{}
		for k, vs := range good {
			v[k] = vs
		}
		v.Set(key, bad)
		if _, _, _, ok := cliParams(v); ok {
			t.Errorf("%s=%q accepted", key, bad)
		}
	}
}
