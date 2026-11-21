package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	files := fstest.MapFS{
		"index.html":               {Data: []byte(`<html><script>self.a=1</script></html>`)},
		"container/index.html":     {Data: []byte(`<html>container</html>`)},
		"404.html":                 {Data: []byte(`<html>missing</html>`)},
		"_next/static/chunks/a.js": {Data: []byte(`console.log(1)`)},
		".hidden":                  {Data: []byte(`secret`)},
	}
	h, err := Handler(files, Config{Title: "Containers"}, "https://containers.example.org")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPages(t *testing.T) {
	h := testHandler(t)
	for _, c := range []struct {
		target, body string
		status       int
	}{
		{"/", "self.a=1", 200},
		{"/container/?name=box", "container", 200},
		{"/container?name=box", "", 301},
		{"/%2Fevil.com%2F..%2Fcontainer?name=box", "", 301},
		{"/%2Fcontainer?name=box", "", 301},
		{"/config.json", `"title":"Containers"`, 200},
		{"/favicon.svg", "<svg", 200},
		{"/.hidden", "missing", 404},
		{"/nope", "missing", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.target, nil))
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.body) {
			t.Errorf("%s = %d %q", c.target, w.Code, w.Body.String())
		}
		if c.status == 301 && w.Header().Get("Location") != "/container/?name=box" {
			t.Errorf("%s redirects to %s", c.target, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'self' wss://containers.example.org", "script-src 'self' 'sha256-", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}
