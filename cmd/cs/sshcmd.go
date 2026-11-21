package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sshBase is the jump through the bastion and the host-key alias (containers' short names would
// clash with other hosts in known_hosts).
func sshBase(name string) []string {
	s := sessionInfo()
	if s.SSH == nil {
		die("the server advertises no SSH bastion")
	}
	return []string{"-J", fmt.Sprintf("%s:%d", s.SSH.Host, s.SSH.Port), "-o", "HostKeyAlias=" + hostKeyAlias(name)}
}

func hostKeyAlias(name string) string {
	u, _ := url.Parse(origin())
	return name + "." + u.Hostname()
}

var sessionRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// tmuxCommand attaches (or makes) a tmux session, set up as the web console's: truecolor, the
// clipboard through OSC 52, no status bar, the mouse. Without tmux in the image: a login shell.
func tmuxCommand(session string) string {
	return `if command -v tmux >/dev/null 2>&1; then exec tmux start-server \; set -g default-terminal tmux-256color \; ` +
		`set -as terminal-features '*:RGB' \; set -as terminal-features '*:clipboard' \; set -g set-clipboard on \; ` +
		`set -g status off \; set -g mouse on \; set -g history-limit 50000 \; set-environment -g COLORTERM truecolor \; ` +
		`new-session -A -s ` + session + `; else exec "${SHELL:-/bin/sh}" -l; fi`
}

func sshCommand(args []string) {
	fs := flag.NewFlagSet("ssh", flag.ExitOnError)
	session := fs.String("s", "console", "tmux session")
	plain := fs.Bool("plain", false, "a shell without tmux")
	name := parseWithName(fs, args)
	if name == "" {
		die("usage: cs ssh <name> [-s session | --plain]")
	}
	if *plain {
		*session = ""
	}
	connect(name, *session, false)
}

// connect lands in the container over SSH, starting it if needed; session "": no tmux.
func connect(name, session string, fresh bool) {
	if session != "" && !sessionRE.MatchString(session) {
		die("a session name is letters, digits, _ and -")
	}
	fresh = ensureRunning(name) || fresh
	saveLast(name)
	args := sshBase(name)
	if session != "" {
		args = append(args, "-t", "dev@"+name, tmuxCommand(session))
	} else {
		args = append(args, "dev@"+name)
	}
	runSSH("ssh", args, fresh)
}

// runSSH runs ssh (or scp), retrying a quick failure while a container that just started may not
// have sshd listening yet, and exits with its status.
func runSSH(program string, args []string, fresh bool) {
	for attempt := 0; ; attempt++ {
		cmd := exec.Command(program, args...)
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

func forward(args []string) {
	if len(args) != 2 {
		die("usage: cs fwd <name> <port>[:<remote port>]")
	}
	name := args[0]
	local, remote, _ := strings.Cut(args[1], ":")
	if remote == "" {
		remote = local
	}
	for _, p := range []string{local, remote} {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			die("%q isn't a port", p)
		}
	}
	fresh := ensureRunning(name)
	fmt.Fprintf(os.Stderr, "Forwarding http://localhost:%s → %s:%s (Ctrl-C stops it)\n", local, name, remote)
	runSSH("ssh", append(sshBase(name), "-N", "-o", "ExitOnForwardFailure=yes", "-L", "127.0.0.1:"+local+":localhost:"+remote, "dev@"+name), fresh)
}

var remotePath = regexp.MustCompile(`^([a-z]([a-z0-9-]{0,50}[a-z0-9])?):(.*)$`)

func copyFiles(args []string) {
	fs := flag.NewFlagSet("cp", flag.ExitOnError)
	recursive := fs.Bool("r", false, "copy directories")
	fs.Parse(args)
	if fs.NArg() != 2 {
		die("usage: cs cp [-r] SRC DST, one side <name>:<path>")
	}
	src, dst := fs.Arg(0), fs.Arg(1)
	var name string
	for _, p := range []*string{&src, &dst} {
		if m := remotePath.FindStringSubmatch(*p); m != nil {
			if name != "" {
				die("copy between your computer and one container")
			}
			name = m[1]
			*p = "dev@" + m[1] + ":" + m[3]
		}
	}
	if name == "" {
		die("one side is <name>:<path>")
	}
	fresh := ensureRunning(name)
	scpArgs := sshBase(name)
	if *recursive {
		scpArgs = append(scpArgs, "-r")
	}
	runSSH("scp", append(scpArgs, src, dst), fresh)
}

// writeSSHConfig writes ~/.ssh/cs_config, a Host <name>.cs per container. Asked for (cs config)
// it's made and how to include it is shown; otherwise it's only kept current if it exists.
func writeSSHConfig(asked bool) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".ssh", "cs_config")
	if _, err := os.Stat(path); err != nil && !asked {
		return
	}
	s := sessionInfo()
	if s.SSH == nil {
		die("the server advertises no SSH bastion")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by cs (cs config) from %s: ssh <name>.cs. Edits are overwritten.\n", origin())
	for _, c := range containers() {
		fmt.Fprintf(&b, "\nHost %s.cs\n  HostName %s\n  User dev\n  ProxyJump %s:%d\n  HostKeyAlias %s\n", c.Name, c.Name, s.SSH.Host, s.SSH.Port, hostKeyAlias(c.Name))
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	must(os.WriteFile(path, []byte(b.String()), 0o600))
	if !asked {
		return
	}
	fmt.Println("Wrote " + path + ".")
	cfg, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if !strings.Contains(string(cfg), "cs_config") {
		fmt.Println("Add this line near the top of ~/.ssh/config (before any Host block) to use it:\n  Include cs_config")
	} else {
		fmt.Println("~/.ssh/config includes it: ssh <name>.cs works.")
	}
}
