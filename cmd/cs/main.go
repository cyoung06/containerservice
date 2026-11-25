// cs is containerservice from a terminal: a live list of your containers to land in over SSH
// (in the same tmux session as the web console, starting the container if it's stopped), quick
// creation from presets, port forwards and copies.
//
//	cs                        the picker (⏎ connect, n new, s start/stop, d delete, / filter)
//	cs new <name> [--preset p] [flags]
//	cs ssh <name> [-s session | --plain]
//	cs fwd <name> <port>[:<remote port>]
//	cs cp [-r] <src> <dst>    one side <name>:<path>
//	cs ls | start | stop | rm <name> | config | login | logout
//
// Sign-in is containerservice's own OAuth-style flow for native apps (internal/auth/cli.go), its
// token in the macOS Keychain; SSH goes through the bastion with your own keys.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
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

func must(err error) {
	if err != nil {
		die("%v", err)
	}
}

func main() {
	args := os.Args[1:]
	cmd := "pick"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "pick":
		pick()
	case "login":
		login()
	case "logout":
		keychainDelete()
		fmt.Println("Signed out (the token is gone from the Keychain).")
	case "ls", "list":
		list()
	case "ssh", "connect":
		sshCommand(args)
	case "new", "create":
		createCommand(args)
	case "fwd", "forward":
		forward(args)
	case "cp", "copy":
		copyFiles(args)
	case "edit":
		edit(one(args, "edit"))
	case "start":
		setRunning(one(args, "start"), true)
	case "stop":
		setRunning(one(args, "stop"), false)
	case "rm", "delete":
		removeCommand(one(args, "rm"))
	case "identity", "identities":
		identityCommand(args)
	case "config":
		writeSSHConfig(true)
	case "version", "--version":
		fmt.Println("cs", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		die("no command %q (cs help)", cmd)
	}
}

const usage = `cs: containerservice from a terminal

  cs                          the picker: ⏎ connect (tmux console), p plain shell, t other tmux session,
                                n new, e edit, s start/stop, d delete, / filter, q quit
  cs new <name>               make a container from a preset (~/.config/cs/presets.toml) and connect
                                --preset NAME  --image REF  --debug  --disk GiB  --home GiB (0: none)
                                --class NAME   --no-claude   --identity new|none|NAME
  cs ssh <name>               connect, in the tmux session "console" (the web console's)
                                -s NAME  another tmux session    --plain  a shell without tmux
  cs fwd <name> 3000[:8080]   forward localhost:3000 to the container's port 8080 (default the same)
  cs cp [-r] SRC DST          copy, one side <name>:<path>
  cs edit <name>              change its settings in $EDITOR (saving restarts it if it runs)
  cs ls                       list
  cs start|stop|rm <name>
  cs identity [ls|new NAME|rm NAME]  identities: what containers are to other services
  cs config                   write ~/.ssh/cs_config: ssh <name>.cs and editors' remote SSH
  cs login|logout             sign in with the browser / forget the token

CS_ORIGIN overrides the server (default https://containers.syeyoung.dev).
`

func one(args []string, cmd string) string {
	if len(args) != 1 {
		die("usage: cs %s <name>", cmd)
	}
	return args[0]
}

func list() {
	cs := containers()
	writeSSHConfig(false)
	if len(cs) == 0 {
		fmt.Println("No containers yet: cs new <name>")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tIMAGE\tSECURITY")
	for _, c := range cs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.State, shortImage(c.Image), map[bool]string{true: "debugging", false: "hardened"}[c.Debug])
	}
	w.Flush()
}

func setRunning(name string, on bool) {
	action := map[bool]string{true: "start", false: "stop"}[on]
	must(call("POST", "/api/containers/"+pathName(name)+"/"+action, nil, nil))
	fmt.Printf("%s: %sed\n", name, map[bool]string{true: "start", false: "stopp"}[on])
}

func removeCommand(name string) {
	get(name) // exists
	fmt.Fprintf(os.Stderr, "Delete the container %s? Its volumes stay. Type its name to confirm: ", name)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(answer) != name {
		die("not deleted")
	}
	c := get(name)
	q := deleteQuery(c)
	must(call("DELETE", "/api/containers/"+pathName(name)+q, nil, nil))
	if q != "" {
		fmt.Printf("%s: deleted, with its identity %s (volumes kept)\n", name, c.Identity)
	} else {
		fmt.Printf("%s: deleted (volumes kept)\n", name)
	}
}

func identityCommand(args []string) {
	if len(args) == 0 || args[0] == "ls" {
		ids := identities()
		if len(ids) == 0 {
			fmt.Println("No identities yet.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tWORN BY\tSUBJECT")
		for _, id := range ids {
			fmt.Fprintf(w, "%s\t%s\t%s\n", id.Name, strings.Join(id.UsedBy, ","), id.Subject)
		}
		w.Flush()
		return
	}
	if len(args) != 2 {
		die("usage: cs identity [ls | new NAME | rm NAME]")
	}
	switch args[0] {
	case "new":
		var id identity
		must(call("POST", "/api/identities", map[string]string{"name": args[1]}, &id))
		fmt.Printf("%s: %s\n", id.Name, id.Subject)
	case "rm":
		must(call("DELETE", "/api/identities/"+pathName(args[1]), nil, nil))
		fmt.Printf("%s: deleted\n", args[1])
	default:
		die("usage: cs identity [ls | new NAME | rm NAME]")
	}
}
