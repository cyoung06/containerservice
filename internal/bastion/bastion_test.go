package bastion

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// start runs a bastion whose every target is an echo server, and returns its address.
func start(t *testing.T, maxConns int) (string, *[]string) {
	t.Helper()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	var targets []string
	srv := New(Config{
		HostKey:  key,
		Target:   func(name string) string { targets = append(targets, name); return echo.Addr().String() },
		MaxConns: maxConns,
		Public:   "bastion.example:2223",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go srv.Serve(l)
	return l.Addr().String(), &targets
}

func dial(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	// Anyone gets in: no auth methods at all.
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "anyone", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestForwardsToAContainersSSH(t *testing.T) {
	addr, targets := start(t, 0)
	c := dial(t, addr)
	conn, err := c.Dial("tcp", "pwn:22")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	conn.Close()
	if len(*targets) != 1 || (*targets)[0] != "pwn" {
		t.Errorf("targets: %v", *targets)
	}
}

func TestRefusesEverythingElse(t *testing.T) {
	addr, targets := start(t, 0)
	c := dial(t, addr)
	for _, target := range []string{"pwn:23", "Pwn:22", "pwn.evil.example:22", "10.0.0.1:22", "pwn-0.containers.containers.svc:22", "-x:22"} {
		if conn, err := c.Dial("tcp", target); err == nil {
			conn.Close()
			t.Errorf("%s: forwarded", target)
		}
	}
	if _, err := c.NewSession(); err == nil || !strings.Contains(err.Error(), "jump host only") {
		t.Errorf("session: %v", err)
	}
	if _, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Error("remote forward allowed")
	}
	if len(*targets) != 0 {
		t.Errorf("dialled %v", *targets)
	}
}

func TestMaxConns(t *testing.T) {
	addr, _ := start(t, 1)
	dial(t, addr)
	if c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "x", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}); err == nil {
		c.Close()
		t.Fatal("a second connection got in")
	}
}
