package containers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"golang.org/x/crypto/ssh"

	"github.com/syeyoung-cloud/containerservice/internal/store"
)

type fakeKeys struct {
	mu              sync.Mutex
	lines           []string
	token, settings string
	ids             []store.Identity
	// vanish: the identity whose record is gone after vanishAfter reads (a delete meanwhile)
	vanish      string
	vanishAfter int
}

func (f *fakeKeys) AddIdentity(_ context.Context, id store.Identity, max int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.ids {
		if x.Name == id.Name {
			return false, store.ErrExists
		}
	}
	if len(f.ids) >= max {
		return false, nil
	}
	f.ids = append(f.ids, id)
	return true, nil
}

func (f *fakeKeys) Identities(context.Context) ([]store.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Identity(nil), f.ids...), nil
}

func (f *fakeKeys) Identity(_ context.Context, name string) (store.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.ids {
		if x.Name == name {
			if f.vanish == name {
				if f.vanishAfter--; f.vanishAfter == 0 {
					f.ids = append(f.ids[:i], f.ids[i+1:]...)
				}
			}
			return x, nil
		}
	}
	return store.Identity{}, store.ErrNotFound
}

func (f *fakeKeys) DeleteIdentity(_ context.Context, name string) (store.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.ids {
		if x.Name == name {
			f.ids = append(f.ids[:i], f.ids[i+1:]...)
			return x, nil
		}
	}
	return store.Identity{}, store.ErrNotFound
}

func (f *fakeKeys) ClaudeConfig(context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, f.settings, nil
}

func (f *fakeKeys) KeyLines(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...), nil
}

func newService(t *testing.T, objects ...runtime.Object) (*Service, *fake.Clientset, *fakeKeys) {
	t.Helper()
	client := fake.NewClientset(objects...)
	keys := &fakeKeys{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := New(ctx, client, Config{Namespace: "cs", PullSecret: "pull"}, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, client, keys
}

// eventually waits for the informers to see a change, on the service's own change events.
func eventually(t *testing.T, s *Service, cond func() bool) {
	t.Helper()
	ch, done := s.Subscribe()
	defer done()
	timeout := time.After(5 * time.Second)
	for !cond() {
		select {
		case <-ch:
		case <-timeout:
			t.Fatal("the informers never saw the change")
		}
	}
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestCheck(t *testing.T) {
	ok := Spec{Name: "pwn", Identity: "pwn", NewIdentity: true, Image: "oci.syeyoung.dev/me/pwn:1", Mounts: []Mount{{Path: "/work", Volume: "work"}, {Path: "/tmp/x", Temp: true, SizeMiB: 512}}}
	if err := ok.Check(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
	bad := map[string]Spec{
		"uppercase name":   {Name: "Pwn", Image: "ubuntu"},
		"long name":        {Name: "a23456789012345678901234567890123456789012345678901234", Image: "ubuntu"},
		"trailing dash":    {Name: "pwn-", Image: "ubuntu"},
		"no image":         {Name: "pwn", Identity: "pwn"},
		"no identity":      {Name: "pwn", Image: "ubuntu"},
		"image with space": {Name: "pwn", Image: "ubuntu 24.04"},
		"relative path":    {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "work", Volume: "w"}}},
		"root path":        {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/", Volume: "w"}}},
		"unclean path":     {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a/../b", Volume: "w"}}},
		"keys path":        {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/etc/containerservice", Volume: "w"}}},
		"above keys path":  {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/etc", Volume: "w"}}},
		"twice":            {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w"}, {Path: "/a", Temp: true}}},
		"both kinds":       {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", Temp: true}}},
		"neither kind":     {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a"}}},
		"sized volume":     {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", SizeMiB: 5}}},
		"tiny memory":      {Name: "pwn", Image: "ubuntu", MemoryMiB: 10},
		"huge disk":        {Name: "pwn", Image: "ubuntu", DiskGiB: 101},
		"claude path":      {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/etc/claude-code", Volume: "w"}}},
		"login path":       {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/etc/containerservice-claude", Volume: "w"}}},
		"negative disk":    {Name: "pwn", Image: "ubuntu", DiskGiB: -1},
		"host key path":    {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/etc/containerservice-host/x", Volume: "w"}}},
		"new without size": {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", New: &NewVolume{}}}},
		"new temp":         {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", Temp: true, New: &NewVolume{SizeGiB: 1}}}},
		"new read-only":    {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", ReadOnly: true, New: &NewVolume{SizeGiB: 1}}}},
		"new twice":        {Name: "pwn", Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "w", New: &NewVolume{SizeGiB: 1}}, {Path: "/b", Volume: "w", New: &NewVolume{SizeGiB: 1}}}},
	}
	for what, sp := range bad {
		if err := sp.Check(); status(err) != http.StatusBadRequest {
			t.Errorf("%s: got %v, want a 400", what, err)
		}
	}
}

func TestCreateMakesTheStatefulSet(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "cs", Labels: map[string]string{ManagedBy: managedByValue}}}
	s, client, _ := newService(t, pvc)
	sp := Spec{Name: "pwn", Image: "oci.syeyoung.dev/me/pwn:1", CPUMillis: 2000, MemoryMiB: 4096, DiskGiB: 30, Debug: true, ClaudeLogin: true,
		Identity: "pwn", NewIdentity: true,
		Mounts: []Mount{{Path: "/work", Volume: "work"}, {Path: "/scratch", Temp: true, SizeMiB: 1024}}}
	c, err := s.Create(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "pwn-0.containers.cs.svc" || !c.Running {
		t.Errorf("view: %+v", c)
	}
	set, err := client.AppsV1().StatefulSets("cs").Get(context.Background(), "pwn", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := set.Spec.Template.Spec
	if *set.Spec.Replicas != 1 || set.Spec.ServiceName != ServiceName {
		t.Errorf("replicas %d, service %q", *set.Spec.Replicas, set.Spec.ServiceName)
	}
	saName := pod.ServiceAccountName
	if *pod.HostUsers || !regexp.MustCompile(`^identity-pwn-[a-z0-9]{8}$`).MatchString(saName) || !*pod.AutomountServiceAccountToken {
		t.Errorf("pod: hostUsers %v, service account %q", *pod.HostUsers, saName)
	}
	if c.Identity != "pwn" || c.Subject != "system:serviceaccount:cs:"+saName {
		t.Errorf("identity %q, subject %q", c.Identity, c.Subject)
	}
	if _, err := client.CoreV1().ServiceAccounts("cs").Get(context.Background(), saName, metav1.GetOptions{}); err != nil {
		t.Errorf("service account: %v", err)
	}
	role, err := client.RbacV1().Roles("cs").Get(context.Background(), saName, metav1.GetOptions{})
	if err != nil || len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0].Resources, []string{"serviceaccounts/token"}) ||
		!reflect.DeepEqual(role.Rules[0].ResourceNames, []string{saName}) || !reflect.DeepEqual(role.Rules[0].Verbs, []string{"create"}) {
		t.Errorf("role: %v %+v", err, role)
	}
	rb, err := client.RbacV1().RoleBindings("cs").Get(context.Background(), saName, metav1.GetOptions{})
	if err != nil || rb.RoleRef.Name != saName || rb.Subjects[0].Name != saName {
		t.Errorf("role binding: %v %+v", err, rb)
	}
	sc := pod.Containers[0].SecurityContext
	if sc.Privileged != nil || !reflect.DeepEqual(sc.Capabilities.Add, []corev1.Capability{"SYS_PTRACE"}) || sc.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Errorf("security context: %+v", sc)
	}
	if len(pod.ImagePullSecrets) != 1 || pod.ImagePullSecrets[0].Name != "pull" {
		t.Errorf("pull secrets: %v", pod.ImagePullSecrets)
	}
	sp.NewIdentity = false // not read back
	if got := specOf(set); !reflect.DeepEqual(got, sp) {
		t.Errorf("spec doesn't read back:\n got %+v\nwant %+v", got, sp)
	}
	key, err := client.CoreV1().Secrets("cs").Get(context.Background(), "pwn-hostkey", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if o := key.OwnerReferences; len(o) != 1 || o[0].Kind != "StatefulSet" || o[0].Name != "pwn" || o[0].UID != set.UID {
		t.Errorf("host key owner: %+v", o)
	}
	if _, err := ssh.ParsePrivateKey(key.Data["ssh_host_ed25519_key"]); err != nil {
		t.Errorf("host key: %v", err)
	}
	found := false
	for _, v := range pod.Volumes {
		found = found || (v.Secret != nil && v.Secret.SecretName == "pwn-hostkey")
	}
	if !found {
		t.Error("the pod doesn't mount its host key")
	}
	if _, err := s.Create(context.Background(), sp); status(err) != http.StatusConflict {
		t.Errorf("second create: %v, want a 409", err)
	}
}

func TestHardenedByDefault(t *testing.T) {
	s, client, _ := newService(t)
	if _, err := s.Create(context.Background(), Spec{Name: "agent", Identity: "agent", NewIdentity: true, Image: "ubuntu"}); err != nil {
		t.Fatal(err)
	}
	set, err := client.AppsV1().StatefulSets("cs").Get(context.Background(), "agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sc := set.Spec.Template.Spec.Containers[0].SecurityContext
	if sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || len(sc.Capabilities.Add) != 0 ||
		!reflect.DeepEqual(sc.Capabilities.Drop, []corev1.Capability{"NET_RAW", "MKNOD"}) || sc.Privileged != nil {
		t.Errorf("security context: %+v", sc)
	}
	if _, ok := set.Spec.Template.Labels[DebugLabel]; ok {
		t.Error("a hardened pod carries the debug label")
	}
	got := specOf(set)
	if got.Debug || got.DiskGiB != DefaultDiskGiB {
		t.Errorf("read back: debug %v, disk %d GiB (want the default %d)", got.Debug, got.DiskGiB, DefaultDiskGiB)
	}
	if q := set.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage]; q.Value() != DefaultDiskGiB<<30 {
		t.Errorf("ephemeral-storage limit %s", q.String())
	}
	if got.ClaudeLogin {
		t.Error("claude login without asking")
	}
	if q := set.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceEphemeralStorage]; q.Value() != 1<<30 {
		t.Errorf("ephemeral-storage request %s, want 1Gi", q.String())
	}
}

func TestSyncClaude(t *testing.T) {
	s, client, keys := newService(t)
	ctx := context.Background()
	keys.token, keys.settings = "sk-ant-oat01-x", `{"model":"opus"}`
	for i := 0; i < 2; i++ { // the second run finds both current
		if err := s.SyncClaude(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sec, _ := client.CoreV1().Secrets("cs").Get(ctx, ClaudeLoginSecret, metav1.GetOptions{})
	cm, _ := client.CoreV1().ConfigMaps("cs").Get(ctx, ClaudeSettingsConfigMap, metav1.GetOptions{})
	if string(sec.Data["token"]) != keys.token || cm.Data["console.json"] != keys.settings {
		t.Fatalf("secret %q, configmap %q", sec.Data["token"], cm.Data["console.json"])
	}
	keys.token, keys.settings = "", ""
	if err := s.SyncClaude(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ = client.CoreV1().Secrets("cs").Get(ctx, ClaudeLoginSecret, metav1.GetOptions{})
	cm, _ = client.CoreV1().ConfigMaps("cs").Get(ctx, ClaudeSettingsConfigMap, metav1.GetOptions{})
	if len(sec.Data["token"]) != 0 || cm.Data["console.json"] != "{}" {
		t.Fatalf("after clearing: secret %q, configmap %q", sec.Data["token"], cm.Data["console.json"])
	}
}

func TestFuseMounts(t *testing.T) {
	s, client, _ := newService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Spec{Name: "both", Identity: "both", NewIdentity: true, Image: "ubuntu", FuseMounts: true, Debug: true}); status(err) != http.StatusBadRequest {
		t.Errorf("debug + fuse: %v", err)
	}
	for _, debug := range []bool{false} {
		name := "fuse"
		if _, err := s.Create(ctx, Spec{Name: name, Image: "ubuntu", FuseMounts: true, Debug: debug, Identity: name, NewIdentity: true}); err != nil {
			t.Fatal(err)
		}
		set, _ := client.AppsV1().StatefulSets("cs").Get(ctx, name, metav1.GetOptions{})
		c := set.Spec.Template.Spec.Containers[0]
		sc := c.SecurityContext
		if !slices.Contains(sc.Capabilities.Add, "SYS_ADMIN") || sc.Privileged != nil {
			t.Errorf("%s: caps %+v", name, sc.Capabilities)
		}
		if sc.AppArmorProfile == nil || *sc.AppArmorProfile.LocalhostProfile != FuseProfile {
			t.Errorf("%s: apparmor %+v", name, sc.AppArmorProfile)
		}
		wantSeccomp := corev1.SeccompProfileTypeLocalhost
		if debug {
			wantSeccomp = corev1.SeccompProfileTypeUnconfined
		}
		if sc.SeccompProfile.Type != wantSeccomp || (!debug && *sc.SeccompProfile.LocalhostProfile != "containerservice-fuse.json") {
			t.Errorf("%s: seccomp %+v", name, sc.SeccompProfile)
		}
		if q := c.Resources.Limits[FuseResource]; q.Value() != 1 {
			t.Errorf("%s: no /dev/fuse requested", name)
		}
		if !specOf(set).FuseMounts {
			t.Errorf("%s: doesn't read back", name)
		}
	}
}

func TestUpdate(t *testing.T) {
	s, client, _ := newService(t)
	ctx := context.Background()
	created, err := s.Create(ctx, Spec{Name: "box", Identity: "box", NewIdentity: true, Image: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	before := created.Subject
	eventually(t, s, func() bool { _, err := s.Get("box"); return err == nil })
	if err := s.SetRunning(ctx, "box", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { c, _ := s.Get("box"); return !c.Running })
	if _, err := s.Update(ctx, "box", Spec{Name: "other", Identity: "other", NewIdentity: true, Image: "ubuntu"}); status(err) != http.StatusBadRequest {
		t.Errorf("rename: %v", err)
	}
	if _, err := s.Update(ctx, "nope", Spec{Name: "nope", Identity: "nope", NewIdentity: true, Image: "ubuntu"}); status(err) != http.StatusNotFound {
		t.Errorf("missing: %v", err)
	}
	next := Spec{Name: "box", Image: "debian", Debug: true, ClaudeLogin: true, DiskGiB: 30, Identity: "box",
		Mounts: []Mount{{Path: "/home/dev", Volume: "box-home", New: &NewVolume{SizeGiB: 5}}}}
	c, err := s.Update(ctx, "box", next)
	if err != nil {
		t.Fatal(err)
	}
	if c.Running {
		t.Error("a stopped container started on edit")
	}
	set, _ := client.AppsV1().StatefulSets("cs").Get(ctx, "box", metav1.GetOptions{})
	if before == "" || set.Spec.Template.Spec.ServiceAccountName == "" || !*set.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Errorf("identity: %q before, %q after", before, set.Spec.Template.Spec.ServiceAccountName)
	}
	got := specOf(set)
	next.Mounts[0].New = nil
	if !reflect.DeepEqual(got, next) {
		t.Errorf("after edit:\n got %+v\nwant %+v", got, next)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("cs").Get(ctx, "box-home", metav1.GetOptions{}); err != nil {
		t.Errorf("the new volume: %v", err)
	}
}

func TestIdentities(t *testing.T) {
	s, client, _ := newService(t)
	ctx := context.Background()
	// Every container wears one.
	if _, err := s.Create(ctx, Spec{Name: "plain", Image: "ubuntu"}); status(err) != http.StatusBadRequest {
		t.Errorf("a container without an identity: %v", err)
	}
	// A new identity, then a second container wearing it, then refusals.
	a, err := s.Create(ctx, Spec{Name: "a", Image: "ubuntu", Identity: "shared", NewIdentity: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Spec{Name: "b", Image: "ubuntu", Identity: "shared", NewIdentity: true}); status(err) != http.StatusConflict {
		t.Errorf("new identity with a taken name: %v", err)
	}
	if _, err := s.Create(ctx, Spec{Name: "b", Image: "ubuntu", Identity: "nope"}); status(err) != http.StatusBadRequest {
		t.Errorf("no such identity: %v", err)
	}
	b, err := s.Create(ctx, Spec{Name: "b", Image: "ubuntu", Identity: "shared"})
	if err != nil || b.Subject != a.Subject {
		t.Fatalf("existing identity: %v (%q vs %q)", err, b.Subject, a.Subject)
	}
	eventually(t, s, func() bool { u, _ := s.identityUsers(); return len(u["shared"]) == 2 })
	ids, _ := s.Identities(ctx)
	if len(ids) != 1 || !reflect.DeepEqual(ids[0].UsedBy, []string{"a", "b"}) {
		t.Errorf("identities: %+v", ids)
	}
	if err := s.DeleteIdentity(ctx, "shared", ""); status(err) != http.StatusConflict {
		t.Errorf("deleting a worn identity: %v", err)
	}
	// Deleting a with its identity is refused (b still wears it); a is gone anyway.
	if err := s.Delete(ctx, "a", true); status(err) != http.StatusConflict {
		t.Errorf("delete a with identity: %v", err)
	}
	eventually(t, s, func() bool { _, err := s.Get("a"); return err != nil })
	// b is the last: deleting it with its identity removes the identity and its account.
	if err := s.Delete(ctx, "b", true); err != nil {
		t.Fatalf("delete b with identity: %v", err)
	}
	if ids, _ := s.Identities(ctx); len(ids) != 0 {
		t.Errorf("identity left: %+v", ids)
	}
	sa := strings.TrimPrefix(a.Subject, "system:serviceaccount:cs:")
	if _, err := client.CoreV1().ServiceAccounts("cs").Get(ctx, sa, metav1.GetOptions{}); err == nil {
		t.Error("the account is still there")
	}
	// Made again under the same name: another account.
	again, _ := s.CreateIdentity(ctx, "shared")
	if again.ServiceAccount == sa {
		t.Error("a subject was used again")
	}
}

func TestAttachRacingDelete(t *testing.T) {
	s, client, keys := newService(t)
	ctx := context.Background()
	id, err := s.CreateIdentity(ctx, "racy")
	if err != nil {
		t.Fatal(err)
	}
	// Create reads it twice (the check, then the attach) before making its objects, then once more:
	// deleted between the attach and that last read.
	keys.vanish, keys.vanishAfter = "racy", 2
	if _, err := s.Create(ctx, Spec{Name: "c", Image: "ubuntu", Identity: "racy"}); err == nil {
		t.Fatal("attached an identity deleted meanwhile")
	}
	if _, err := client.CoreV1().ServiceAccounts("cs").Get(ctx, id.ServiceAccount, metav1.GetOptions{}); err == nil {
		t.Error("left an account without a record")
	}
	if _, err := client.AppsV1().StatefulSets("cs").Get(ctx, "c", metav1.GetOptions{}); err == nil {
		t.Error("made the container anyway")
	}
}

func TestCreateMakesNewVolumes(t *testing.T) {
	zfs := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "zfs"}, Provisioner: "zfs.csi.openebs.io"}
	static := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "static"}, Provisioner: noProvisioner}
	taken := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "taken", Namespace: "cs", Labels: map[string]string{ManagedBy: managedByValue}}}
	s, client, _ := newService(t, zfs, static, taken)
	ctx := context.Background()
	refused := map[string]Mount{
		"existing name": {Path: "/home/dev", Volume: "taken", New: &NewVolume{StorageClass: "zfs", SizeGiB: 1}},
		"static class":  {Path: "/home/dev", Volume: "home", New: &NewVolume{StorageClass: "static", SizeGiB: 1}},
		"no such class": {Path: "/home/dev", Volume: "home", New: &NewVolume{StorageClass: "nope", SizeGiB: 1}},
	}
	for what, m := range refused {
		// The good new volume comes first: nothing at all may be made when a later mount is refused.
		sp := Spec{Name: "dev", Identity: "dev", NewIdentity: true, Image: "ubuntu", Mounts: []Mount{{Path: "/a", Volume: "first", New: &NewVolume{StorageClass: "zfs", SizeGiB: 1}}, m}}
		if _, err := s.Create(ctx, sp); status(err) < 400 || status(err) > 409 {
			t.Errorf("%s: got %v, want a refusal", what, err)
		}
	}
	if pvcs, _ := client.CoreV1().PersistentVolumeClaims("cs").List(ctx, metav1.ListOptions{}); len(pvcs.Items) != 1 {
		t.Fatalf("refused creates made volumes: %d", len(pvcs.Items))
	}
	sp := Spec{Name: "dev", Identity: "dev", NewIdentity: true, Image: "ubuntu", Mounts: []Mount{{Path: "/home/dev", Volume: "home", New: &NewVolume{StorageClass: "zfs", SizeGiB: 20}}}}
	c, err := s.Create(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("cs").Get(ctx, "home", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; gib(q) != 20 || ptr.Deref(pvc.Spec.StorageClassName, "") != "zfs" {
		t.Errorf("volume: %v in %v", q.String(), pvc.Spec.StorageClassName)
	}
	if want := []Mount{{Path: "/home/dev", Volume: "home"}}; !reflect.DeepEqual(c.Mounts, want) {
		t.Errorf("mounts read back %+v, want %+v", c.Mounts, want)
	}
}

func TestCreateRefusesMissingVolume(t *testing.T) {
	s, _, _ := newService(t)
	_, err := s.Create(context.Background(), Spec{Name: "pwn", Identity: "pwn", NewIdentity: true, Image: "ubuntu", Mounts: []Mount{{Path: "/work", Volume: "nope"}}})
	if status(err) != http.StatusBadRequest {
		t.Fatalf("got %v, want a 400", err)
	}
}

func TestStopStartDelete(t *testing.T) {
	s, client, _ := newService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Spec{Name: "box", Identity: "box", NewIdentity: true, Image: "ubuntu"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { _, err := s.Get("box"); return err == nil })
	if err := s.SetRunning(ctx, "box", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { c, _ := s.Get("box"); return !c.Running && c.State == "stopped" })
	if err := s.SetRunning(ctx, "box", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { c, _ := s.Get("box"); return c.Running && c.State == "pending" })
	if _, err := s.Pod("box"); status(err) != http.StatusConflict {
		t.Errorf("terminal into a pod-less container: %v, want a 409", err)
	}
	if err := s.Delete(ctx, "box", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().StatefulSets("cs").Get(ctx, "box", metav1.GetOptions{}); err == nil {
		t.Error("still there after delete")
	}
	if err := s.Delete(ctx, "nope", false); status(err) != http.StatusNotFound {
		t.Errorf("deleting a missing container: %v", err)
	}
}

func TestPodState(t *testing.T) {
	s, client, _ := newService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Spec{Name: "box", Identity: "box", NewIdentity: true, Image: "nope:1"}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "box-0", Namespace: "cs", Labels: map[string]string{ManagedBy: managedByValue, NameLabel: "box"}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{Name: MainContainer,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}}}}},
	}
	if _, err := client.CoreV1().Pods("cs").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { c, _ := s.Get("box"); return c.State == "ImagePullBackOff" })
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.5", ContainerStatuses: []corev1.ContainerStatus{{Name: MainContainer, RestartCount: 2,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	if _, err := client.CoreV1().Pods("cs").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool {
		c, _ := s.Get("box")
		return c.State == "running" && c.IP == "10.0.0.5" && c.Restarts == 2
	})
	if p, err := s.Pod("box"); err != nil || p != "box-0" {
		t.Errorf("Pod: %q, %v", p, err)
	}
}

func TestVolumes(t *testing.T) {
	sc := &storagev1.StorageClass{Provisioner: "zfs.csi.openebs.io", ObjectMeta: metav1.ObjectMeta{Name: "zfs", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}, AllowVolumeExpansion: ptr.To(true)}
	static := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "static"}, Provisioner: noProvisioner}
	s, client, _ := newService(t, sc, static)
	ctx := context.Background()
	if _, err := s.CreateVolume(ctx, "work", "zfs", 0); status(err) != http.StatusBadRequest {
		t.Errorf("0 GiB: %v", err)
	}
	if _, err := s.CreateVolume(ctx, "work", "static", 1); status(err) != http.StatusBadRequest {
		t.Errorf("class without a provisioner: %v", err)
	}
	if _, err := s.CreateVolume(ctx, "work", "nope", 1); status(err) != http.StatusBadRequest {
		t.Errorf("missing class: %v", err)
	}
	if _, err := s.CreateVolume(ctx, "Work", "zfs", 1); status(err) != http.StatusBadRequest {
		t.Errorf("bad name: %v", err)
	}
	v, err := s.CreateVolume(ctx, "work", "zfs", 10)
	if err != nil || v.SizeGiB != 10 || v.StorageClass != "zfs" {
		t.Fatalf("create: %+v, %v", v, err)
	}
	if _, err := s.CreateVolume(ctx, "work", "zfs", 10); status(err) != http.StatusConflict {
		t.Errorf("duplicate: %v", err)
	}
	eventually(t, s, func() bool { _, err := s.volume("work"); return err == nil })
	if err := s.ResizeVolume(ctx, "work", 5); status(err) != http.StatusBadRequest {
		t.Errorf("shrink: %v", err)
	}
	if err := s.ResizeVolume(ctx, "work", 20); err != nil {
		t.Fatal(err)
	}
	pvc, _ := client.CoreV1().PersistentVolumeClaims("cs").Get(ctx, "work", metav1.GetOptions{})
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "20Gi" {
		t.Errorf("after resize: %s", q.String())
	}
	if _, err := s.Create(ctx, Spec{Name: "box", Identity: "box", NewIdentity: true, Image: "ubuntu", Mounts: []Mount{{Path: "/work", Volume: "work"}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool {
		vs, _ := s.Volumes()
		return len(vs) == 1 && reflect.DeepEqual(vs[0].UsedBy, []string{"box"})
	})
	if err := s.DeleteVolume(ctx, "work"); status(err) != http.StatusConflict {
		t.Errorf("deleting a mounted volume: %v, want a 409", err)
	}
	if err := s.Delete(ctx, "box", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, s, func() bool { _, err := s.Get("box"); return err != nil })
	if err := s.DeleteVolume(ctx, "work"); err != nil {
		t.Errorf("deleting a free volume: %v", err)
	}
	classes, err := s.StorageClasses(ctx)
	if err != nil || len(classes) != 1 || !classes[0].Default || !classes[0].Expandable {
		t.Errorf("classes: %+v, %v", classes, err)
	}
}

func TestSyncKeys(t *testing.T) {
	s, client, keys := newService(t)
	ctx := context.Background()
	read := func() string {
		sec, err := client.CoreV1().Secrets("cs").Get(ctx, KeysSecret, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return string(sec.Data["authorized_keys"])
	}
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "" {
		t.Errorf("no keys: %q", got)
	}
	keys.lines = []string{"ssh-ed25519 AAAA1", "ssh-ed25519 AAAA2"}
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "ssh-ed25519 AAAA1\nssh-ed25519 AAAA2\n" {
		t.Errorf("two keys: %q", got)
	}
}

// A write refused because the Secret changed since it was read is done again from a fresh read.
func TestSyncKeysRetriesOnConflict(t *testing.T) {
	s, client, keys := newService(t)
	ctx := context.Background()
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	keys.lines = []string{"ssh-ed25519 AAAA1"}
	refused := false
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !refused {
			refused = true
			return true, nil, errConflict()
		}
		return false, nil, nil
	})
	if err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := client.CoreV1().Secrets("cs").Get(ctx, KeysSecret, metav1.GetOptions{})
	if !refused || string(sec.Data["authorized_keys"]) != "ssh-ed25519 AAAA1\n" {
		t.Errorf("refused %v, content %q", refused, sec.Data["authorized_keys"])
	}
}

func errConflict() error {
	return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, KeysSecret, errors.New("changed"))
}
