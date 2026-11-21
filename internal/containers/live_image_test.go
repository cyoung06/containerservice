//go:build live

package containers

// CONTAINERSERVICE_LIVE_IMAGE=<ref> go test -tags live -run LiveImage ./internal/containers/ (with
// CONTAINERSERVICE_LIVE_NAMESPACE): a container of an image from images/, with a new volume at
// /home/dev, reached over SSH (through kubectl port-forward) as dev; its host key and home stay
// across a stop and start.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"golang.org/x/crypto/ssh"
)

func TestLiveImage(t *testing.T) {
	ns, image := os.Getenv("CONTAINERSERVICE_LIVE_NAMESPACE"), os.Getenv("CONTAINERSERVICE_LIVE_IMAGE")
	if ns == "" || image == "" {
		t.Skip("CONTAINERSERVICE_LIVE_NAMESPACE or CONTAINERSERVICE_LIVE_IMAGE is not set")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	sshPub, _ := ssh.NewPublicKey(pub)
	// CONTAINERSERVICE_LIVE_KEY_FILE: log in with this private key instead (to SSH in by hand too).
	if f := os.Getenv("CONTAINERSERVICE_LIVE_KEY_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if signer, err = ssh.ParsePrivateKey(raw); err != nil {
			t.Fatal(err)
		}
		sshPub = signer.PublicKey()
	}
	keys := &fakeKeys{lines: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " live@test"}}
	s, err := New(ctx, client, Config{Namespace: ns, PullSecret: os.Getenv("CONTAINERSERVICE_LIVE_PULL_SECRET")}, keys, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, Spec{Name: "liveimg", Image: image, MemoryMiB: 1024,
		Mounts: []Mount{{Path: "/home/dev", Volume: "liveimg-home", New: &NewVolume{StorageClass: "zfs", SizeGiB: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if os.Getenv("CONTAINERSERVICE_LIVE_KEEP") != "" {
			return
		}
		s.Delete(context.Background(), "liveimg")
		eventually(t, s, func() bool { _, err := s.Get("liveimg"); return err != nil })
		eventually(t, s, func() bool { return len(mustUsedBy(s)["liveimg-home"]) == 0 })
		if err := s.DeleteVolume(context.Background(), "liveimg-home"); err != nil {
			t.Errorf("deleting the volume: %v", err)
		}
	}()
	secret, err := client.CoreV1().Secrets(ns).Get(ctx, HostKeySecret("liveimg"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantHost, _, _, _, err := ssh.ParseAuthorizedKey(secret.Data["ssh_host_ed25519_key.pub"])
	if err != nil {
		t.Fatal(err)
	}
	waitState := func(want string) {
		ch, done := s.Subscribe()
		defer done()
		for {
			c, _ := s.Get("liveimg")
			if c.State == want {
				return
			}
			t.Logf("state %s %s", c.State, c.Message)
			select {
			case <-ch:
			case <-ctx.Done():
				t.Fatalf("never %s: %+v", want, c)
			}
		}
	}
	// sshd logs "Server listening" (-e) once it accepts connections.
	waitSSHD := func() {
		logs := exec.CommandContext(ctx, "kubectl", "-n", ns, "logs", "-f", "pod/liveimg-0")
		out, _ := logs.StdoutPipe()
		if err := logs.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { logs.Process.Kill(); logs.Wait() }()
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			if strings.Contains(lines.Text(), "Server listening") {
				return
			}
		}
		t.Fatal("sshd never listened")
	}
	session := func(cmd string) string {
		// To sshd through kubectl exec and the image's nc (kubectl port-forward has lost output).
		nc := exec.CommandContext(ctx, "kubectl", "-n", ns, "exec", "-i", "liveimg-0", "--", "nc", "-q0", "127.0.0.1", "22")
		in, _ := nc.StdinPipe()
		out, _ := nc.StdoutPipe()
		if err := nc.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { in.Close(); nc.Wait() }()
		c, chans, reqs, err := ssh.NewClientConn(pipeConn{out, in}, "liveimg", &ssh.ClientConfig{User: "dev",
			Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(wantHost)})
		var conn *ssh.Client
		if err == nil {
			conn = ssh.NewClient(c, chans, reqs)
		}
		if err != nil {
			logs, _ := exec.Command("kubectl", "-n", ns, "logs", "pod/liveimg-0").CombinedOutput()
			diag, _ := exec.Command("kubectl", "-n", ns, "exec", "liveimg-0", "--", "sh", "-c",
				"ls -lan /etc/containerservice/ /etc/containerservice/..data/ /etc/ / | head -40; grep dev /etc/passwd /etc/shadow; id dev").CombinedOutput()
			t.Fatalf("ssh: %v\nsshd log:\n%s\n%s", err, logs, diag)
		}
		defer conn.Close()
		sess, _ := conn.NewSession()
		defer sess.Close()
		var stdout, stderr bytes.Buffer
		sess.Stdout, sess.Stderr = &stdout, &stderr
		if err := sess.Run("echo BEGIN; " + cmd + "; echo END"); err != nil {
			t.Fatalf("%s: %v (%s%s)", cmd, err, stdout.String(), stderr.String())
		}
		got, ok := strings.CutPrefix(stdout.String(), "BEGIN\n")
		got, ok2 := strings.CutSuffix(got, "END\n")
		if !ok || !ok2 {
			logs, _ := exec.Command("kubectl", "-n", ns, "logs", "pod/liveimg-0").CombinedOutput()
			t.Fatalf("%s: stdout %q, stderr %q\nsshd log:\n%s", cmd, stdout.String(), stderr.String(), logs)
		}
		return got
	}
	waitState("running")
	waitSSHD()
	got := session(`whoami; sudo -n id -u; pwd; ls -A; echo kept > ~/marker; sync; cat ~/marker; df -h ~; bash -lc 'command -v pwndbg pwn claude go gh uv node one_gadget r2 || true'`)
	t.Logf("first session:\n%s", got)
	if !strings.HasPrefix(got, "dev\n0\n/home/dev\n") || !strings.Contains(got, ".bashrc") {
		t.Errorf("first session: %q", got)
	}
	if err := s.SetRunning(ctx, "liveimg", false); err != nil {
		t.Fatal(err)
	}
	waitState("stopped")
	if err := s.SetRunning(ctx, "liveimg", true); err != nil {
		t.Fatal(err)
	}
	waitState("running")
	waitSSHD()
	// The same host key (FixedHostKey) and the home folder kept.
	if got := session("cat ~/marker; ls -la ~; df -h ~; mount | grep /home/dev"); !strings.HasPrefix(got, "kept\n") {
		t.Errorf("after a restart: %q", got)
	}
}

// pipeConn is a net.Conn over a command's stdout and stdin.
type pipeConn struct {
	io.Reader
	io.WriteCloser
}

func (pipeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (pipeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (pipeConn) SetDeadline(time.Time) error      { return nil }
func (pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeConn) SetWriteDeadline(time.Time) error { return nil }

func mustUsedBy(s *Service) map[string][]string {
	u, _ := s.usedBy()
	return u
}
