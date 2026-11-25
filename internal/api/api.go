// Package api is the HTTP API under /api/ (docs/api.md).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/cyoung06/containerservice/internal/auth"
	"github.com/cyoung06/containerservice/internal/containers"
	"github.com/cyoung06/containerservice/internal/store"
	"github.com/cyoung06/containerservice/internal/terminal"
)

// Keys is the key table.
type Keys interface {
	AddKey(ctx context.Context, k store.Key, max int) (bool, error)
	Keys(ctx context.Context) ([]store.Key, error)
	DeleteKey(ctx context.Context, id string) error
	Claude(ctx context.Context) (store.Claude, error)
	SetClaudeToken(ctx context.Context, token string) error
	SetClaudeSettings(ctx context.Context, settings string) error
}

// Sessions tells who a request is from.
type Sessions interface {
	Session(r *http.Request) (auth.Session, bool)
}

// API serves /api/.
type API struct {
	sessions   Sessions
	containers *containers.Service
	keys       Keys
	terminal   *terminal.Proxy
	origin     string
	ssh        *SSH
	log        *slog.Logger
	// Images are suggested in the new-container form; any image may still be given.
	Images []string
}

// SSH is the bastion's public address, shown with each container.
type SSH struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// MaxKeys bounds the key list.
const MaxKeys = 50

// New is the API for origin (the public origin, no trailing slash; writes and terminals from
// another Origin are refused). sshAddr may be nil (no bastion advertised).
func New(s Sessions, c *containers.Service, k Keys, t *terminal.Proxy, origin string, sshAddr *SSH, log *slog.Logger) *API {
	return &API{sessions: s, containers: c, keys: k, terminal: t, origin: origin, ssh: sshAddr, log: log}
}

// Routes adds the API's endpoints to mux.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", a.read(a.session))
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/containers", a.read(a.list))
	mux.HandleFunc("GET /api/containers/{name}", a.read(a.get))
	mux.HandleFunc("POST /api/containers", a.write(a.create))
	mux.HandleFunc("PATCH /api/containers/{name}", a.write(a.update))
	mux.HandleFunc("POST /api/containers/{name}/start", a.write(a.running(true)))
	mux.HandleFunc("POST /api/containers/{name}/stop", a.write(a.running(false)))
	mux.HandleFunc("DELETE /api/containers/{name}", a.write(a.remove))
	mux.HandleFunc("GET /api/containers/{name}/terminal", a.term)
	mux.HandleFunc("GET /api/volumes", a.read(a.volumes))
	mux.HandleFunc("POST /api/volumes", a.write(a.createVolume))
	mux.HandleFunc("PATCH /api/volumes/{name}", a.write(a.resizeVolume))
	mux.HandleFunc("DELETE /api/volumes/{name}", a.write(a.deleteVolume))
	mux.HandleFunc("GET /api/storage-classes", a.read(a.storageClasses))
	mux.HandleFunc("GET /api/keys", a.read(a.listKeys))
	mux.HandleFunc("POST /api/keys", a.write(a.addKey))
	mux.HandleFunc("DELETE /api/keys/{id}", a.write(a.deleteKey))
	mux.HandleFunc("GET /api/identities", a.read(a.identities))
	mux.HandleFunc("POST /api/identities", a.write(a.createIdentity))
	mux.HandleFunc("DELETE /api/identities/{name}", a.write(a.deleteIdentity))
	mux.HandleFunc("GET /api/claude", a.read(a.claude))
	mux.HandleFunc("PUT /api/claude/token", a.write(a.setClaudeToken))
	mux.HandleFunc("PUT /api/claude/settings", a.write(a.setClaudeSettings))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusNotFound, detail("No such endpoint."))
	})
}

type handler func(r *http.Request, s auth.Session) (status int, body any, err error)

func detail(message string) map[string]string { return map[string]string{"detail": message} }

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body != nil {
		json.NewEncoder(w).Encode(body)
	}
}

func bad(format string, args ...any) error {
	return &containers.Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

func (a *API) fail(w http.ResponseWriter, err error) {
	var e *containers.Error
	if errors.As(err, &e) {
		reply(w, e.Status, detail(e.Message))
		return
	}
	a.log.Error("api", "err", err)
	reply(w, http.StatusInternalServerError, detail("Something went wrong on the server."))
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, h handler) {
	s, ok := a.sessions.Session(r)
	if !ok {
		reply(w, http.StatusUnauthorized, detail("Sign in first."))
		return
	}
	status, body, err := h(r, s)
	if err != nil {
		a.fail(w, err)
		return
	}
	reply(w, status, body)
}

func (a *API) read(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, h) }
}

// sameOrigin is the CSRF guard: X-Requested-With can't be sent cross-origin without a CORS
// preflight this server never answers, and an Origin, when there is one, must be ours.
func (a *API) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != a.origin {
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// write guards a change: a bearer token (cs) needs no CSRF guard; the cookie does.
func (a *API) write(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasBearer(r) && (r.Header.Get("X-Requested-With") != "XMLHttpRequest" || !a.sameOrigin(r)) {
			reply(w, http.StatusForbidden, detail("Cross-origin requests are refused."))
			return
		}
		a.serve(w, r, h)
	}
}

// decode reads a JSON body of at most 16 KiB into v, refusing unknown fields.
func decode(r *http.Request, v any, what string) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad("The request isn't valid JSON for %s.", what)
	}
	return nil
}

func (a *API) session(_ *http.Request, s auth.Session) (int, any, error) {
	return http.StatusOK, map[string]any{"username": s.Username, "expires": s.Expires.UTC(), "ssh": a.ssh, "images": a.images()}, nil
}

func (a *API) images() []string {
	if a.Images == nil {
		return []string{}
	}
	return a.Images
}

// events is a server-sent event stream: "changed" whenever a container or volume changes, for the
// UI to read the lists again. It ends when the session expires.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	s, ok := a.sessions.Session(r)
	if !ok {
		reply(w, http.StatusUnauthorized, detail("Sign in first."))
		return
	}
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})
	rc.SetReadDeadline(time.Time{})
	ch, done := a.containers.Subscribe()
	defer done()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "event: changed\ndata: {}\n\n")
	rc.Flush()
	expiry := time.NewTimer(time.Until(s.Expires))
	defer expiry.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expiry.C:
			return
		case <-ch:
			if _, err := fmt.Fprint(w, "event: changed\ndata: {}\n\n"); err != nil {
				return
			}
			rc.Flush()
		}
	}
}

func (a *API) list(*http.Request, auth.Session) (int, any, error) {
	cs, err := a.containers.List()
	return http.StatusOK, map[string]any{"containers": cs}, err
}

func (a *API) get(r *http.Request, _ auth.Session) (int, any, error) {
	c, err := a.containers.Get(r.PathValue("name"))
	return http.StatusOK, c, err
}

func (a *API) create(r *http.Request, _ auth.Session) (int, any, error) {
	var sp containers.Spec
	if err := decode(r, &sp, "a new container"); err != nil {
		return 0, nil, err
	}
	sp.Name, sp.Image = strings.TrimSpace(sp.Name), strings.TrimSpace(sp.Image)
	c, err := a.containers.Create(r.Context(), sp)
	return http.StatusCreated, c, err
}

// update replaces a container's settings (the whole spec; the name can't change).
func (a *API) update(r *http.Request, _ auth.Session) (int, any, error) {
	var sp containers.Spec
	if err := decode(r, &sp, "a container's settings"); err != nil {
		return 0, nil, err
	}
	sp.Image = strings.TrimSpace(sp.Image)
	c, err := a.containers.Update(r.Context(), r.PathValue("name"), sp)
	return http.StatusOK, c, err
}

func (a *API) running(on bool) handler {
	return func(r *http.Request, _ auth.Session) (int, any, error) {
		return http.StatusNoContent, nil, a.containers.SetRunning(r.Context(), r.PathValue("name"), on)
	}
}

func (a *API) remove(r *http.Request, _ auth.Session) (int, any, error) {
	// ?identity=delete: its identity too, if no other container wears it.
	return http.StatusNoContent, nil, a.containers.Delete(r.Context(), r.PathValue("name"), r.URL.Query().Get("identity") == "delete")
}

// shell is what the terminal runs: the tmux session "console" (made or attached, so a terminal
// left and reopened finds what ran in it, until the container stops), as the image's
// CONTAINERSERVICE_USER if it names one, in a login shell; without tmux, a login bash (or sh).
// It says it does truecolor (xterm.js does), through su (-w) and tmux (RGB), and passes copies to
// the browser's clipboard (OSC 52: tmux set-clipboard, the terminal's clipboard addon).
var shell = []string{"/bin/sh", "-c", `export TERM=xterm-256color COLORTERM=truecolor
t='if command -v tmux >/dev/null 2>&1; then exec tmux start-server \; set -g default-terminal tmux-256color \; set -as terminal-features xterm-256color:RGB \; set -as terminal-features xterm-256color:clipboard \; set -g set-clipboard on \; set -g status off \; set -g mouse on \; set -g history-limit 50000 \; new-session -A -s console; elif command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi'
u=${CONTAINERSERVICE_USER:-}
if [ -n "$u" ] && [ "$(id -u)" = 0 ] && id "$u" >/dev/null 2>&1 && command -v su >/dev/null 2>&1; then exec su -l -w COLORTERM "$u" -c "$t"; fi
exec sh -c "$t"`}

// term serves a container's terminal websocket.
func (a *API) term(w http.ResponseWriter, r *http.Request) {
	s, ok := a.sessions.Session(r)
	if !ok {
		reply(w, http.StatusUnauthorized, detail("Sign in first."))
		return
	}
	// Browsers send cookies on cross-site websockets too, and always send Origin on them: it must be ours.
	if r.Header.Get("Origin") != a.origin {
		reply(w, http.StatusForbidden, detail("Cross-origin requests are refused."))
		return
	}
	if !terminal.IsUpgrade(r) {
		reply(w, http.StatusBadRequest, detail("This is a websocket with the subprotocol "+strings.Join(terminal.Subprotocols, " or ")+"."))
		return
	}
	pod, err := a.containers.Pod(r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	q := url.Values{"container": {containers.MainContainer}, "stdin": {"true"}, "stdout": {"true"}, "tty": {"true"}, "command": shell}
	path := "/api/v1/namespaces/" + url.PathEscape(a.containers.Namespace()) + "/pods/" + url.PathEscape(pod) + "/exec"
	err = a.terminal.Serve(r.Context(), w, r, path, q, s.Expires)
	var up *terminal.UpstreamError
	var hj *terminal.Hijacked
	switch {
	case err == nil:
	case errors.As(err, &hj):
		a.log.Info("terminal ended", "container", r.PathValue("name"), "err", err)
	case errors.As(err, &up):
		a.log.Warn("terminal refused", "container", r.PathValue("name"), "err", err)
		reply(w, http.StatusBadGateway, detail("Kubernetes refused the terminal."))
	default:
		a.log.Error("terminal", "container", r.PathValue("name"), "err", err)
		reply(w, http.StatusBadGateway, detail("The terminal couldn't be opened."))
	}
}

func (a *API) volumes(*http.Request, auth.Session) (int, any, error) {
	vs, err := a.containers.Volumes()
	return http.StatusOK, map[string]any{"volumes": vs}, err
}

func (a *API) createVolume(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		Name         string `json:"name"`
		StorageClass string `json:"storageClass"`
		SizeGiB      int    `json:"sizeGiB"`
	}
	if err := decode(r, &b, "a new volume"); err != nil {
		return 0, nil, err
	}
	v, err := a.containers.CreateVolume(r.Context(), strings.TrimSpace(b.Name), b.StorageClass, b.SizeGiB)
	return http.StatusCreated, v, err
}

func (a *API) resizeVolume(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		SizeGiB int `json:"sizeGiB"`
	}
	if err := decode(r, &b, "a volume's size"); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, a.containers.ResizeVolume(r.Context(), r.PathValue("name"), b.SizeGiB)
}

func (a *API) deleteVolume(r *http.Request, _ auth.Session) (int, any, error) {
	return http.StatusNoContent, nil, a.containers.DeleteVolume(r.Context(), r.PathValue("name"))
}

func (a *API) storageClasses(r *http.Request, _ auth.Session) (int, any, error) {
	cs, err := a.containers.StorageClasses(r.Context())
	return http.StatusOK, map[string]any{"storageClasses": cs}, err
}

// Key is an SSH key as the API shows it.
type Key struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Type        string    `json:"type"`
	Label       string    `json:"label"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toKey(k store.Key) Key {
	out := Key{ID: k.ID, Label: k.Label, CreatedAt: k.CreatedAt.UTC()}
	if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Line)); err == nil {
		out.Type, out.Fingerprint = pk.Type(), ssh.FingerprintSHA256(pk)
	}
	return out
}

func (a *API) listKeys(r *http.Request, _ auth.Session) (int, any, error) {
	ks, err := a.keys.Keys(r.Context())
	out := make([]Key, 0, len(ks))
	for _, k := range ks {
		out = append(out, toKey(k))
	}
	return http.StatusOK, map[string]any{"keys": out}, err
}

var control = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func (a *API) addKey(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	if err := decode(r, &b, "a key"); err != nil {
		return 0, nil, err
	}
	pk, comment, err := containers.ParseKey(b.Key)
	if err != nil {
		return 0, nil, err
	}
	label := strings.TrimSpace(b.Label)
	if label == "" {
		label = comment
	}
	if control.MatchString(label) || utf8.RuneCountInString(label) > 63 {
		return 0, nil, bad("A label is at most 63 characters, without control characters.")
	}
	k := store.Key{ID: containers.KeyID(pk), Line: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), Label: label}
	added, err := a.keys.AddKey(r.Context(), k, MaxKeys)
	switch {
	case errors.Is(err, store.ErrExists):
		return 0, nil, &containers.Error{Status: http.StatusConflict, Message: "That key is listed already."}
	case err != nil:
		return 0, nil, err
	case !added:
		return 0, nil, &containers.Error{Status: http.StatusConflict, Message: fmt.Sprintf("At most %d keys.", MaxKeys)}
	}
	if err := a.containers.SyncKeys(r.Context()); err != nil {
		return 0, nil, err
	}
	k.CreatedAt = time.Now()
	return http.StatusCreated, toKey(k), nil
}

func (a *API) deleteKey(r *http.Request, _ auth.Session) (int, any, error) {
	err := a.keys.DeleteKey(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil, &containers.Error{Status: http.StatusNotFound, Message: "There is no such key."}
	}
	if err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, a.containers.SyncKeys(r.Context())
}

// claude is the Claude Code login and shared settings; the token itself is never sent back.
func (a *API) claude(r *http.Request, _ auth.Session) (int, any, error) {
	c, err := a.keys.Claude(r.Context())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"tokenSet": c.Token != "", "tokenAt": c.TokenAt, "settings": c.Settings, "settingsAt": c.SettingsAt}, nil
}

// MaxClaudeSettings bounds the settings JSON.
const MaxClaudeSettings = 64 << 10

var tokenRE = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)

func (a *API) setClaudeToken(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		Token string `json:"token"`
	}
	if err := decode(r, &b, "a token"); err != nil {
		return 0, nil, err
	}
	b.Token = strings.TrimSpace(b.Token)
	if b.Token != "" && !tokenRE.MatchString(b.Token) {
		return 0, nil, bad("A token is up to 512 printable characters without spaces (from claude setup-token); empty removes it.")
	}
	if err := a.keys.SetClaudeToken(r.Context(), b.Token); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, a.containers.SyncClaude(r.Context())
}

func (a *API) setClaudeSettings(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		Settings string `json:"settings"`
	}
	if err := decode(r, &b, "settings"); err != nil {
		return 0, nil, err
	}
	b.Settings = strings.TrimSpace(b.Settings)
	if len(b.Settings) > MaxClaudeSettings {
		return 0, nil, bad("Settings are at most %d KiB.", MaxClaudeSettings>>10)
	}
	if b.Settings != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(b.Settings), &obj); err != nil || obj == nil {
			return 0, nil, bad("Settings are a JSON object, like {\"model\": \"opus\"}; empty removes them.")
		}
	}
	if err := a.keys.SetClaudeSettings(r.Context(), b.Settings); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, a.containers.SyncClaude(r.Context())
}

func (a *API) identities(r *http.Request, _ auth.Session) (int, any, error) {
	ids, err := a.containers.Identities(r.Context())
	return http.StatusOK, map[string]any{"identities": ids}, err
}

func (a *API) createIdentity(r *http.Request, _ auth.Session) (int, any, error) {
	var b struct {
		Name string `json:"name"`
	}
	if err := decode(r, &b, "an identity"); err != nil {
		return 0, nil, err
	}
	id, err := a.containers.CreateIdentity(r.Context(), strings.TrimSpace(b.Name))
	return http.StatusCreated, id, err
}

func (a *API) deleteIdentity(r *http.Request, _ auth.Session) (int, any, error) {
	return http.StatusNoContent, nil, a.containers.DeleteIdentity(r.Context(), r.PathValue("name"), "")
}
