package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Preset is a way to make a container (~/.config/cs/presets.toml).
type Preset struct {
	// Image: a short name of one of the server's suggested images ("claude" for
	// oci.syeyoung.dev/containers/claude:<tag>), or a full reference.
	Image string `toml:"image"`
	// Home: GiB of a new volume <name>-home at /home/dev (0: none); Class its storage class.
	Home  int    `toml:"home"`
	Class string `toml:"class"`
	Disk  int    `toml:"disk"` // GiB of the container's own disk (0: the server's default)
	Debug bool   `toml:"debug"`
	// ClaudeLogin: the shared Claude token.
	ClaudeLogin bool    `toml:"claude_login"`
	CPU         float64 `toml:"cpu"`    // cores (0: no limit)
	Memory      int     `toml:"memory"` // MiB (0: no limit)
}

type presetFile struct {
	Default string            `toml:"default"`
	Presets map[string]Preset `toml:"presets"`
}

const defaultPresets = `# cs presets: "cs new <name>" uses the default one, "cs new <name> --preset pwn" another;
# the picker's "n" offers them all. Flags override a preset's values.
#   image         a short name of one of the server's suggested images (claude, pwn, base) or a full ref
#   home          GiB of a new volume <name>-home at /home/dev (0: none); class: its storage class
#   disk          GiB of the container's own disk (root filesystem and temporary folders)
#   debug         ptrace and no seccomp filter (else hardened)
#   claude_login  the shared Claude token (set on the console's Claude page)
#   cpu, memory   limits: cores, MiB (0: none)
default = "claude"

[presets.claude]
image = "claude"
home = 20
disk = 20
claude_login = true

[presets.pwn]
image = "pwn"
home = 10
disk = 20
debug = true

[presets.base]
image = "base"
home = 10
disk = 20
`

func presetsPath() string { return filepath.Join(configDir(), "presets.toml") }

// loadPresets reads the presets, writing the defaults the first time.
func loadPresets() presetFile {
	path := presetsPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		os.MkdirAll(configDir(), 0o700)
		os.WriteFile(path, []byte(defaultPresets), 0o600)
	}
	var f presetFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		die("%s: %v", path, err)
	}
	if len(f.Presets) == 0 {
		die("%s has no presets", path)
	}
	if _, ok := f.Presets[f.Default]; !ok {
		die("%s: the default preset %q isn't there", path, f.Default)
	}
	return f
}

// presetNames is the default first, then the rest by name.
func (f presetFile) names() []string {
	names := []string{f.Default}
	for _, n := range sortedKeys(f.Presets) {
		if n != f.Default {
			names = append(names, n)
		}
	}
	return names
}

// resolveImage turns a short name into one of the server's suggested images.
func resolveImage(image string, suggestions []string) (string, error) {
	if strings.ContainsAny(image, "/:@") {
		return image, nil
	}
	for _, ref := range suggestions {
		if shortImage(ref) == image {
			return ref, nil
		}
	}
	return "", fmt.Errorf("no suggested image is called %q (the server suggests %s)", image, strings.Join(suggestions, ", "))
}

// shortImage: oci.syeyoung.dev/containers/claude:2026.11.22-3 → claude.
func shortImage(ref string) string {
	ref = ref[strings.LastIndex(ref, "/")+1:]
	if i := strings.IndexAny(ref, ":@"); i >= 0 {
		ref = ref[:i]
	}
	return ref
}

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$`)

// specFor makes the spec of a new container from a preset.
func specFor(name string, p Preset, s session) spec {
	if !nameRE.MatchString(name) {
		die("a name is 1–52 lowercase letters, digits and dashes, starting with a letter")
	}
	image, err := resolveImage(p.Image, s.Images)
	must(err)
	sp := spec{Name: name, Image: image, Mounts: []mount{}, DiskGiB: p.Disk, Debug: p.Debug, ClaudeLogin: p.ClaudeLogin,
		CPUMillis: int(p.CPU * 1000), MemoryMiB: p.Memory}
	if p.Home > 0 {
		sp.Mounts = append(sp.Mounts, mount{Path: "/home/dev", Volume: name + "-home", New: &newVolume{StorageClass: p.Class, SizeGiB: p.Home}})
	}
	return sp
}

func createCommand(args []string) {
	presets := loadPresets()
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	presetName := fs.String("preset", presets.Default, "preset")
	image := fs.String("image", "", "image (short name or reference)")
	debug := fs.Bool("debug", false, "debugging: ptrace, no seccomp filter")
	disk := fs.Int("disk", 0, "disk limit, GiB")
	home := fs.Int("home", 0, "new volume <name>-home at /home/dev, GiB (0: none)")
	class := fs.String("class", "", "the home volume's storage class")
	noClaude := fs.Bool("no-claude", false, "without the shared Claude login")
	name := parseWithName(fs, args)
	p, ok := presets.Presets[*presetName]
	if !ok {
		die("no preset %q in %s", *presetName, presetsPath())
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "image":
			p.Image = *image
		case "debug":
			p.Debug = *debug
		case "disk":
			p.Disk = *disk
		case "home":
			p.Home = *home
		case "class":
			p.Class = *class
		case "no-claude":
			p.ClaudeLogin = !*noClaude
		}
	})
	if name == "" {
		die("usage: cs new <name> [--preset NAME] [flags]")
	}
	create(name, *presetName, p)
}

// parseWithName parses flags that may come before or after one positional name.
func parseWithName(fs *flag.FlagSet, args []string) string {
	var name string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return name
		}
		if name != "" {
			die("one name only")
		}
		name, args = args[0], args[1:]
	}
}

func create(name, presetName string, p Preset) {
	s := sessionInfo()
	sp := specFor(name, p, s)
	extra := ""
	if sp.Debug {
		extra = ", debugging"
	}
	fmt.Fprintf(os.Stderr, "Making %s (%s preset: %s%s)…\n", name, presetName, shortImage(sp.Image), extra)
	must(call("POST", "/api/containers", sp, nil))
	writeSSHConfig(false)
	waitRunning(name)
	connect(name, "console", true)
}
