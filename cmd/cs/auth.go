package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"
)

// --- the token, in the macOS Keychain --------------------------------------------------------

const keychainService = "containerservice-cs"

func keychainGet() string {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-a", origin(), "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func keychainSet(token string) {
	// -U updates an existing item. (The token is on the command line of security(1) for a moment;
	// it's this user's own process.)
	if out, err := exec.Command("security", "add-generic-password", "-U", "-s", keychainService, "-a", origin(), "-w", token).CombinedOutput(); err != nil {
		die("saving the token in the Keychain: %v %s", err, out)
	}
}

func keychainDelete() {
	exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", origin()).Run()
}

// --- sign-in ---------------------------------------------------------------------------------

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// login is OAuth 2.0 for native apps (RFC 8252) against containerservice: the authorization code
// grant with PKCE S256 and a loopback redirect on a port the OS picks.
func login() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		die("listening for the browser: %v", err)
	}
	defer l.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", l.Addr().(*net.TCPAddr).Port)
	verifier, state := randomString(48), randomString(24)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {"cs"}, "redirect_uri": {redirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {state}}
	u := origin() + "/oauth/authorize?" + q.Encode()
	type result struct{ code, err string }
	results := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		if r.URL.Path != "/callback" || v.Get("state") != state {
			http.Error(w, "This isn't the sign-in cs is waiting for.", http.StatusBadRequest)
			return
		}
		res := result{code: v.Get("code"), err: v.Get("error")}
		if res.err != "" {
			fmt.Fprintln(w, "cs wasn't signed in ("+res.err+"). You can close this tab.")
		} else {
			fmt.Fprintln(w, "cs is signed in. You can close this tab.")
		}
		select {
		case results <- res:
		default:
		}
	})}
	go srv.Serve(l)
	defer srv.Close()
	fmt.Println("Opening the browser to sign in. If it doesn't open, go to:\n  " + u)
	exec.Command("open", u).Run()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var res result
	select {
	case res = <-results:
	case <-time.After(10 * time.Minute):
		die("no sign-in within 10 minutes")
	case <-ctx.Done():
		die("cancelled")
	}
	if res.err != "" || res.code == "" {
		die("not signed in: %s", strings.TrimPrefix(res.err+" ", " "))
	}
	resp, err := http.PostForm(origin()+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {res.code},
		"redirect_uri": {redirect}, "client_id": {"cs"}, "code_verifier": {verifier}})
	if err != nil {
		die("exchanging the code: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Username         string `json:"username"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 200 || got.AccessToken == "" {
		die("exchanging the code: %s %s %s", resp.Status, got.Error, got.ErrorDescription)
	}
	keychainSet(got.AccessToken)
	fmt.Printf("Signed in as %s until %s.\n", got.Username, time.Now().Add(time.Duration(got.ExpiresIn)*time.Second).Format("2006-01-02 15:04"))
}
