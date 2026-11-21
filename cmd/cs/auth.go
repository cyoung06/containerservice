package main

import (
	"bytes"
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
	"strconv"
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

func login() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		die("listening for the browser: %v", err)
	}
	defer l.Close()
	verifier, state := randomString(48), randomString(24)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"port": {strconv.Itoa(l.Addr().(*net.TCPAddr).Port)}, "challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "state": {state}}
	u := origin() + "/cli/login?" + q.Encode()
	codes := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.URL.Query().Get("state") != state || r.URL.Query().Get("code") == "" {
			http.Error(w, "This isn't the sign-in cs is waiting for.", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "cs is signed in. You can close this tab.")
		select {
		case codes <- r.URL.Query().Get("code"):
		default:
		}
	})}
	go srv.Serve(l)
	defer srv.Close()
	fmt.Println("Opening the browser to sign in. If it doesn't open, go to:\n  " + u)
	exec.Command("open", u).Run()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var code string
	select {
	case code = <-codes:
	case <-time.After(10 * time.Minute):
		die("no sign-in within 10 minutes")
	case <-ctx.Done():
		die("cancelled")
	}
	body, _ := json.Marshal(map[string]string{"code": code, "verifier": verifier})
	resp, err := http.Post(origin()+"/cli/token", "application/json", bytes.NewReader(body))
	if err != nil {
		die("exchanging the code: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Token, Username, Detail string
		Expires                 time.Time
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 200 || got.Token == "" {
		die("exchanging the code: %s %s", resp.Status, got.Detail)
	}
	keychainSet(got.Token)
	fmt.Printf("Signed in as %s until %s.\n", got.Username, got.Expires.Local().Format("2006-01-02 15:04"))
}
