package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/cyoung06/containerservice/internal/auth"
	"github.com/cyoung06/containerservice/internal/containers"
	"github.com/cyoung06/containerservice/internal/store"
)

const origin = "https://containers.example.org"

type signedIn bool

func (s signedIn) Session(*http.Request) (auth.Session, bool) {
	return auth.Session{Username: "me", Expires: time.Now().Add(time.Hour)}, bool(s)
}

type memKeys struct {
	mu   sync.Mutex
	keys []store.Key
}

func (m *memKeys) AddKey(_ context.Context, k store.Key, max int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.keys {
		if x.ID == k.ID {
			return false, store.ErrExists
		}
	}
	if len(m.keys) >= max {
		return false, nil
	}
	m.keys = append(m.keys, k)
	return true, nil
}
func (m *memKeys) Keys(context.Context) ([]store.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Key(nil), m.keys...), nil
}
func (m *memKeys) DeleteKey(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.keys {
		if x.ID == id {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}
func (m *memKeys) KeyLines(ctx context.Context) ([]string, error) {
	ks, _ := m.Keys(ctx)
	var out []string
	for _, k := range ks {
		out = append(out, k.Line)
	}
	return out, nil
}

func setup(t *testing.T, in bool) (http.Handler, *fake.Clientset) {
	t.Helper()
	client := fake.NewClientset()
	keys := &memKeys{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := containers.New(ctx, client, containers.Config{Namespace: "cs"}, keys, log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(signedIn(in), svc, keys, nil, origin, &SSH{Host: "ssh.example.org", Port: 2223}, log).Routes(mux)
	return mux, client
}

func do(h http.Handler, method, path, body string, xhr bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if xhr {
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSignInRequired(t *testing.T) {
	h, _ := setup(t, false)
	if w := do(h, "GET", "/api/containers", "", false); w.Code != http.StatusUnauthorized {
		t.Errorf("got %d", w.Code)
	}
}

func TestWritesNeedXHR(t *testing.T) {
	h, _ := setup(t, true)
	if w := do(h, "POST", "/api/volumes", `{"name":"v","sizeGiB":1}`, false); w.Code != http.StatusForbidden {
		t.Errorf("without X-Requested-With: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/volumes", strings.NewReader(`{"name":"v","sizeGiB":1}`))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", w.Code)
	}
}

func TestKeysReachTheSecret(t *testing.T) {
	h, client := setup(t, true)
	line := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEGNmrnkVmSJ/ttGVwIQlprdIUmVHMLNm5rd4r3RpaQS me@laptop"
	w := do(h, "POST", "/api/keys", `{"key":"`+line+`"}`, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	var k Key
	json.Unmarshal(w.Body.Bytes(), &k)
	if k.Label != "me@laptop" || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Errorf("key: %+v", k)
	}
	if w := do(h, "POST", "/api/keys", `{"key":"`+line+`"}`, true); w.Code != http.StatusConflict {
		t.Errorf("duplicate: %d", w.Code)
	}
	sec, err := client.CoreV1().Secrets("cs").Get(context.Background(), containers.KeysSecret, metav1.GetOptions{})
	if err != nil || !strings.HasPrefix(string(sec.Data["authorized_keys"]), "ssh-ed25519 AAAAC3") {
		t.Fatalf("secret: %v %q", err, sec.Data)
	}
	if w := do(h, "DELETE", "/api/keys/"+k.ID, "", true); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	sec, _ = client.CoreV1().Secrets("cs").Get(context.Background(), containers.KeysSecret, metav1.GetOptions{})
	if len(sec.Data["authorized_keys"]) != 0 {
		t.Errorf("after delete: %q", sec.Data["authorized_keys"])
	}
	if w := do(h, "POST", "/api/keys", `{"key":"ssh-rsa AAAA"}`, true); w.Code != http.StatusBadRequest {
		t.Errorf("bad key: %d", w.Code)
	}
}

func TestCreateContainer(t *testing.T) {
	h, _ := setup(t, true)
	w := do(h, "POST", "/api/containers", `{"name":"pwn","image":"ubuntu:24.04","mounts":[{"path":"/scratch","temp":true}]}`, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/containers", `{"name":"pwn","image":"ubuntu:24.04","bogus":1}`, true); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", w.Code)
	}
	if w := do(h, "GET", "/api/containers/nope", "", false); w.Code != http.StatusNotFound {
		t.Errorf("missing: %d", w.Code)
	}
	if w := do(h, "GET", "/api/session", "", false); !strings.Contains(w.Body.String(), `"port":2223`) {
		t.Errorf("session: %s", w.Body)
	}
}
