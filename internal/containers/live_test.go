//go:build live

package containers

// go test -tags live -run Live ./internal/containers/ with CONTAINERSERVICE_LIVE_NAMESPACE set: runs
// the real thing against the cluster in $KUBECONFIG (a scratch namespace it may fill and empty).

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLive(t *testing.T) {
	ns := os.Getenv("CONTAINERSERVICE_LIVE_NAMESPACE")
	if ns == "" {
		t.Skip("CONTAINERSERVICE_LIVE_NAMESPACE is not set")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	keys := &fakeKeys{lines: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEGNmrnkVmSJ/ttGVwIQlprdIUmVHMLNm5rd4r3RpaQS live@test"}}
	s, err := New(ctx, client, Config{Namespace: ns}, keys, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVolume(ctx, "livevol", "zfs", 1); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteVolume(context.Background(), "livevol")
	eventually(t, s, func() bool { _, err := s.volume("livevol"); return err == nil })
	_, err = s.Create(ctx, Spec{Name: "livebox", Image: "docker.io/library/nginx:1.27-alpine", MemoryMiB: 256,
		Mounts: []Mount{{Path: "/data", Volume: "livevol"}, {Path: "/scratch", Temp: true, SizeMiB: 64}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Delete(context.Background(), "livebox")
		eventually(t, s, func() bool { _, err := s.Get("livebox"); return err != nil })
	}()
	ch, done := s.Subscribe()
	defer done()
	for {
		c, _ := s.Get("livebox")
		t.Logf("state %s %s", c.State, c.Message)
		if c.State == "running" {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("never ran: %+v", c)
		}
	}
	pod, _ := s.Pod("livebox")
	run := func(cmd ...string) string {
		out, err := exec.CommandContext(ctx, "kubectl", append([]string{"-n", ns, "exec", pod, "-c", MainContainer, "--"}, cmd...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v (%s)", cmd, err, out)
		}
		return string(out)
	}
	t.Logf("uid_map: %s", run("cat", "/proc/self/uid_map"))
	if got := run("cat", "/etc/containerservice/authorized_keys"); !strings.Contains(got, "live@test") {
		t.Errorf("authorized_keys: %q", got)
	}
	if got := run("sh", "-c", "grep -E '^(CapEff|Seccomp):' /proc/self/status"); !strings.Contains(got, "Seccomp:\t0") {
		t.Errorf("seccomp should be off: %q", got)
	}
	if got := run("sh", "-c", "echo hi > /data/x && cat /data/x && df -h /data /scratch | tail -2"); !strings.HasPrefix(got, "hi") {
		t.Errorf("volume: %q", got)
	}
	// Key change reaches the Secret.
	keys.lines = nil
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := client.CoreV1().Secrets(ns).Get(ctx, KeysSecret, metav1.GetOptions{})
	if len(sec.Data["authorized_keys"]) != 0 {
		t.Errorf("secret after removing keys: %q", sec.Data["authorized_keys"])
	}
	if err := s.SetRunning(ctx, "livebox", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { c, _ := s.Get("livebox"); return c.State == "stopped" })
}
