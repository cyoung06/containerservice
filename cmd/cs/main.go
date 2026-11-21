// cs is containerservice from a terminal: pick a container and land in it over SSH (starting it
// if it's stopped), or make one and land in it.
//
//	cs                 pick a container and connect
//	cs new [name]      make a container (claude preset) and connect
//	cs ssh <name>      connect
//	cs ls | start | stop | rm <name>
//	cs login | logout  sign in with the browser (the token goes to the macOS Keychain)
//
// Sign-in is containerservice's own OAuth-style flow for native apps (internal/auth/cli.go); SSH
// goes through the bastion with your own keys (the ones in the console).
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

var version = "dev"

func origin() string {
	if o := os.Getenv("CS_ORIGIN"); o != "" {
		return strings.TrimRight(o, "/")
	}
	return "https://containers.syeyoung.dev"
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cs: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	cmd := "pick"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "login":
		login()
	case "logout":
		keychainDelete()
		fmt.Println("Signed out (the token is gone from the Keychain).")
	case "pick":
		pick()
	case "ls", "list":
		list()
	case "ssh", "connect":
		connect(one(args, "ssh"), false)
	case "new", "create":
		create(args)
	case "start":
		setRunning(one(args, "start"), true)
	case "stop":
		setRunning(one(args, "stop"), false)
	case "rm", "delete":
		remove(one(args, "rm"))
	case "version", "--version":
		fmt.Println("cs", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		die("no command %q (cs help)", cmd)
	}
}

const usage = `cs: containerservice from a terminal

  cs                 pick a container and connect (starting it if stopped)
  cs new [name]      make a container and connect; flags:
                       --image REF   (default: the claude image)  --debug  --disk GiB
                       --home GiB    new volume <name>-home at /home/dev (default 20; 0: none)
                       --class NAME  its storage class (default: the cluster's)
                       --no-claude   without the shared Claude login
  cs ssh <name>      connect
  cs ls              list
  cs start|stop|rm <name>
  cs login|logout    sign in with the browser / forget the token

CS_ORIGIN overrides the server (default https://containers.syeyoung.dev).
`

func one(args []string, cmd string) string {
	if len(args) != 1 {
		die("usage: cs %s <name>", cmd)
	}
	return args[0]
}

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

// --- the API ---------------------------------------------------------------------------------

type mount struct {
	Path     string     `json:"path"`
	Volume   string     `json:"volume,omitempty"`
	Temp     bool       `json:"temp,omitempty"`
	ReadOnly bool       `json:"readOnly,omitempty"`
	SizeMiB  int        `json:"sizeMiB,omitempty"`
	New      *newVolume `json:"new,omitempty"`
}

type newVolume struct {
	StorageClass string `json:"storageClass"`
	SizeGiB      int    `json:"sizeGiB"`
}

type spec struct {
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	Mounts      []mount `json:"mounts"`
	CPUMillis   int     `json:"cpuMillis"`
	MemoryMiB   int     `json:"memoryMiB"`
	DiskGiB     int     `json:"diskGiB"`
	Debug       bool    `json:"debug"`
	ClaudeLogin bool    `json:"claudeLogin"`
}

type container struct {
	spec
	Running bool   `json:"running"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type session struct {
	Username string `json:"username"`
	SSH      *struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"ssh"`
	Images []string `json:"images"`
}

type apiError struct {
	status int
	detail string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (%d)", e.detail, e.status) }

// call does a request with the token, signing in first if there's none or it has expired.
func call(method, path string, in, out any) error {
	for attempt := 0; ; attempt++ {
		token := keychainGet()
		if token == "" {
			login()
			token = keychainGet()
		}
		var body io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, origin()+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			fmt.Fprintln(os.Stderr, "The sign-in has expired.")
			keychainDelete()
			continue
		}
		if resp.StatusCode >= 300 {
			var e struct{ Detail string }
			json.Unmarshal(data, &e)
			if e.Detail == "" {
				e.Detail = resp.Status
			}
			return &apiError{resp.StatusCode, e.Detail}
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func must(err error) {
	if err != nil {
		die("%v", err)
	}
}

func containers() []container {
	var got struct{ Containers []container }
	must(call("GET", "/api/containers", nil, &got))
	return got.Containers
}

func get(name string) container {
	var c container
	must(call("GET", "/api/containers/"+url.PathEscape(name), nil, &c))
	return c
}

// waitRunning waits for the container to run, on the server's change events (no polling).
func waitRunning(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin()+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+keychainGet())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("watching for the container: %v", err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	last := ""
	for {
		c := get(name)
		if c.State != last {
			fmt.Fprintf(os.Stderr, "  %s: %s %s\n", name, c.State, c.Message)
			last = c.State
		}
		switch {
		case c.State == "running":
			return
		case strings.Contains(c.State, "BackOff") || c.State == "ErrImagePull" || c.State == "CreateContainerConfigError":
			die("%s won't start: %s %s", name, c.State, c.Message)
		}
		// Next change (the stream also sends a comment now and then to stay open).
		for lines.Scan() && !strings.HasPrefix(lines.Text(), "event:") && !strings.HasPrefix(lines.Text(), "data:") {
		}
		if lines.Err() != nil || ctx.Err() != nil {
			die("%s didn't start within 5 minutes", name)
		}
	}
}

// --- commands --------------------------------------------------------------------------------

func list() {
	cs := containers()
	if len(cs) == 0 {
		fmt.Println("No containers yet: cs new <name>")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tIMAGE")
	for _, c := range cs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.State, c.Image)
	}
	w.Flush()
}

func pick() {
	cs := containers()
	if len(cs) == 0 {
		fmt.Println("No containers yet: cs new <name>")
		return
	}
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for i, c := range cs {
		fmt.Fprintf(w, "  %d\t%s\t%s\t%s\n", i+1, c.Name, c.State, c.Image)
	}
	w.Flush()
	fmt.Fprint(os.Stderr, "Connect to (number or name, n: new): ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer == "n" || answer == "new" {
		create(nil)
		return
	}
	if i, err := strconv.Atoi(answer); err == nil && i >= 1 && i <= len(cs) {
		connect(cs[i-1].Name, false)
		return
	}
	for _, c := range cs {
		if c.Name == answer {
			connect(c.Name, false)
			return
		}
	}
	if answer != "" {
		die("no container %q", answer)
	}
}

func connect(name string, fresh bool) {
	c := get(name)
	if !c.Running {
		fmt.Fprintf(os.Stderr, "Starting %s…\n", name)
		must(call("POST", "/api/containers/"+url.PathEscape(name)+"/start", nil, nil))
		fresh = true
	}
	if c.State != "running" {
		waitRunning(name)
		fresh = true
	}
	var s session
	must(call("GET", "/api/session", nil, &s))
	if s.SSH == nil {
		die("the server advertises no SSH bastion")
	}
	host, _ := url.Parse(origin())
	args := []string{
		"-J", fmt.Sprintf("%s:%d", s.SSH.Host, s.SSH.Port),
		// Containers' short names would clash with other hosts in known_hosts.
		"-o", "HostKeyAlias=" + name + "." + host.Hostname(),
		"dev@" + name,
	}
	// A container that just started may not have sshd listening yet: retry a quick failure.
	for attempt := 0; ; attempt++ {
		cmd := exec.Command("ssh", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		began := time.Now()
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 255 && fresh && attempt < 5 && time.Since(began) < 5*time.Second {
			time.Sleep(time.Second)
			continue
		}
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		must(err)
		return
	}
}

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$`)

func create(args []string) {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	image := fs.String("image", "", "image (default: the claude image the server suggests)")
	debug := fs.Bool("debug", false, "debugging: ptrace, no seccomp filter (else hardened)")
	disk := fs.Int("disk", 20, "disk limit, GiB")
	home := fs.Int("home", 20, "new volume <name>-home at /home/dev, GiB (0: none)")
	class := fs.String("class", "", "the home volume's storage class (default: the cluster's)")
	noClaude := fs.Bool("no-claude", false, "without the shared Claude login")
	// Flags may come before or after the name.
	var name string
	rest := args
	for len(rest) > 0 {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) > 0 {
			if name != "" {
				die("one name only")
			}
			name, rest = rest[0], rest[1:]
		}
	}
	if name == "" {
		fmt.Fprint(os.Stderr, "Name: ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		name = strings.TrimSpace(answer)
	}
	if !nameRE.MatchString(name) {
		die("a name is 1–52 lowercase letters, digits and dashes, starting with a letter")
	}
	var s session
	must(call("GET", "/api/session", nil, &s))
	if *image == "" {
		for _, ref := range s.Images {
			if strings.Contains(ref, "/claude:") {
				*image = ref
				break
			}
		}
		if *image == "" {
			die("the server suggests no claude image: give --image")
		}
	}
	sp := spec{Name: name, Image: *image, Mounts: []mount{}, DiskGiB: *disk, Debug: *debug, ClaudeLogin: !*noClaude}
	if *home > 0 {
		sp.Mounts = append(sp.Mounts, mount{Path: "/home/dev", Volume: name + "-home", New: &newVolume{StorageClass: *class, SizeGiB: *home}})
	}
	fmt.Fprintf(os.Stderr, "Making %s (%s%s)…\n", name, filepath.Base(*image), map[bool]string{true: ", debugging", false: ""}[*debug])
	must(call("POST", "/api/containers", sp, nil))
	waitRunning(name)
	connect(name, true)
}

func setRunning(name string, on bool) {
	action := map[bool]string{true: "start", false: "stop"}[on]
	must(call("POST", "/api/containers/"+url.PathEscape(name)+"/"+action, nil, nil))
	fmt.Printf("%s: %sed\n", name, map[bool]string{true: "start", false: "stopp"}[on])
}

func remove(name string) {
	get(name) // exists
	fmt.Fprintf(os.Stderr, "Delete the container %s? Its volumes stay. Type its name to confirm: ", name)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(answer) != name {
		die("not deleted")
	}
	must(call("DELETE", "/api/containers/"+url.PathEscape(name), nil, nil))
	fmt.Printf("%s: deleted (volumes kept)\n", name)
}
