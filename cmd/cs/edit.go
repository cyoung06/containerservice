package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/BurntSushi/toml"
)

// editable is a container's settings as cs shows them for editing: everything but the name.
type editable struct {
	Image       string      `toml:"image"`
	CPU         float64     `toml:"cpu"`    // cores, 0: no limit
	Memory      int         `toml:"memory"` // MiB, 0: no limit
	Disk        int         `toml:"disk"`   // GiB
	Debug       bool        `toml:"debug"`
	FuseMounts  bool        `toml:"fuse_mounts"`
	ClaudeLogin bool        `toml:"claude_login"`
	Identity    string      `toml:"identity"` // one of yours ("": none); new_identity makes it
	NewIdentity bool        `toml:"new_identity,omitempty"`
	Mounts      []editMount `toml:"mounts"`
}

type editMount struct {
	Path     string `toml:"path"`
	Volume   string `toml:"volume,omitempty"`    // an existing volume
	NewGiB   int    `toml:"new_gib,omitempty"`   // with volume: make it, this size
	Class    string `toml:"class,omitempty"`     // its storage class
	Temp     bool   `toml:"temp,omitempty"`      // a temporary folder
	SizeMiB  int    `toml:"size_mib,omitempty"`  // its cap
	ReadOnly bool   `toml:"read_only,omitempty"` // a volume, read-only
}

const editHeader = `# Settings of %s. Save and quit to apply; saving restarts it if it runs (volumes keep their
# files, what runs in it ends). Empty the file to cancel.
# mounts: an existing volume {path, volume, read_only}, a new one {path, volume, new_gib, class},
# or a temporary folder {path, temp = true, size_mib}. identity: one of yours, "" for none, or a new
# one's name with new_identity = true. debug and fuse_mounts can't both be on.

`

// edit opens the container's settings in $EDITOR and saves what comes back.
func edit(name string) {
	c := get(name)
	e := editable{Image: c.Image, CPU: float64(c.CPUMillis) / 1000, Memory: c.MemoryMiB, Disk: c.DiskGiB,
		Debug: c.Debug, FuseMounts: c.FuseMounts, ClaudeLogin: c.ClaudeLogin, Identity: c.Identity}
	for _, m := range c.Mounts {
		e.Mounts = append(e.Mounts, editMount{Path: m.Path, Volume: m.Volume, Temp: m.Temp, SizeMiB: m.SizeMiB, ReadOnly: m.ReadOnly})
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, editHeader, name)
	must(toml.NewEncoder(&buf).Encode(e))
	f, err := os.CreateTemp("", "cs-"+name+"-*.toml")
	must(err)
	defer os.Remove(f.Name())
	f.Write(buf.Bytes())
	f.Close()
	for {
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", f.Name())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		must(cmd.Run())
		data, _ := os.ReadFile(f.Name())
		if strings.TrimSpace(stripComments(string(data))) == "" {
			fmt.Println("Cancelled.")
			return
		}
		var got editable
		if _, err := toml.Decode(string(data), &got); err != nil {
			if !again(err) {
				return
			}
			continue
		}
		sp := spec{Name: name, Image: got.Image, CPUMillis: int(got.CPU * 1000), MemoryMiB: got.Memory, DiskGiB: got.Disk,
			Debug: got.Debug, FuseMounts: got.FuseMounts, ClaudeLogin: got.ClaudeLogin, Identity: got.Identity, NewIdentity: got.NewIdentity, Mounts: []mount{}}
		for _, m := range got.Mounts {
			mt := mount{Path: m.Path, Volume: m.Volume, Temp: m.Temp, SizeMiB: m.SizeMiB, ReadOnly: m.ReadOnly}
			if m.NewGiB > 0 {
				mt.New = &newVolume{StorageClass: m.Class, SizeGiB: m.NewGiB}
			}
			sp.Mounts = append(sp.Mounts, mt)
		}
		if err := call("PATCH", "/api/containers/"+pathName(name), sp, nil); err != nil {
			if !again(err) {
				return
			}
			continue
		}
		writeSSHConfig(false)
		fmt.Printf("%s: saved.\n", name)
		return
	}
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// again asks whether to edit again after an error.
func again(err error) bool {
	fmt.Fprintf(os.Stderr, "Not saved: %v\nEdit again? [Y/n] ", err)
	var answer string
	fmt.Scanln(&answer)
	return !strings.HasPrefix(strings.ToLower(answer), "n")
}
