package containers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/cyoung06/containerservice/internal/store"
)

// MainContainer is the name of the container in each pod (what the terminal execs into).
const MainContainer = "main"

// Spec is what a container is made from.
type Spec struct {
	Name   string  `json:"name"`
	Image  string  `json:"image"`
	Mounts []Mount `json:"mounts"`
	// CPUMillis and MemoryMiB are limits; 0: none.
	CPUMillis int `json:"cpuMillis"`
	MemoryMiB int `json:"memoryMiB"`
	// DiskGiB caps the container's own disk use (its root filesystem, temporary folders and
	// logs, all on the node's disk); 0 on a create: DefaultDiskGiB. Past it, the pod is evicted.
	DiskGiB int `json:"diskGiB"`
	// Debug is for debuggers: ptrace and no seccomp filter. Without it a container is hardened:
	// the runtime's default seccomp profile, no raw sockets or device nodes, and (a network policy
	// on DebugLabel) no traffic to the other pods of the namespace but from the bastion.
	Debug bool `json:"debug"`
	// ClaudeLogin mounts the owner's Claude Code token at ClaudeLoginPath (the claude image
	// exports it as CLAUDE_CODE_OAUTH_TOKEN). Every container gets the shared settings.
	ClaudeLogin bool `json:"claudeLogin"`
	// FuseMounts lets the container mount FUSE filesystems (sshfs, rclone): /dev/fuse, CAP_SYS_ADMIN
	// in its own user namespace, and the node's containerservice-fuse seccomp and AppArmor profiles,
	// which allow FUSE mounts and nothing more of what SYS_ADMIN would.
	FuseMounts bool `json:"fuseMounts"`
	// Identity is the identity the container wears; every container has one ("" only on
	// containers made before identities, until they're edited). NewIdentity, on a create or an
	// edit: make it first (a name that exists is refused). Never read back.
	Identity    string `json:"identity"`
	NewIdentity bool   `json:"newIdentity,omitempty"`
}

// Mount is a volume (a PVC by name) or a temporary folder (Temp) at Path.
type Mount struct {
	Path     string `json:"path"`
	Volume   string `json:"volume,omitempty"`
	Temp     bool   `json:"temp,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// SizeMiB caps a temporary folder; 0: no cap.
	SizeMiB int `json:"sizeMiB,omitempty"`
	// New, on a create, makes the volume first (it mustn't exist). Never read back.
	New *NewVolume `json:"new,omitempty"`
}

// NewVolume is a volume to make with a container: in StorageClass (empty: the default), SizeGiB.
type NewVolume struct {
	StorageClass string `json:"storageClass"`
	SizeGiB      int    `json:"sizeGiB"`
}

// Container is a container as the API shows it.
type Container struct {
	Spec
	// Running: it should be (1 replica); the pod's own state is State.
	Running bool `json:"running"`
	// State is "stopped", "stopping", "pending", "running", or why the container is waiting
	// (ImagePullBackOff, CrashLoopBackOff, ...).
	State    string `json:"state"`
	Message  string `json:"message,omitempty"`
	Restarts int32  `json:"restarts"`
	Host     string `json:"host"` // the pod's DNS name, what the bastion connects to
	// Subject is that of its identity's tokens (sa-token), what other services grant to.
	Subject   string    `json:"subject,omitempty"`
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

const (
	maxMounts = 16
	maxImage  = 512
	// DefaultDiskGiB and MaxDiskGiB bound DiskGiB.
	DefaultDiskGiB = 20
	MaxDiskGiB     = 100
	// DebugLabel marks a debugging container's pod; the network policy that keeps hardened ones
	// to themselves selects pods without it.
	DebugLabel = "containerservice.syeyoung.dev/debug"
	// FuseResource is /dev/fuse (cmd/fuse-device-plugin); FuseProfile the node's seccomp
	// (FuseProfile+".json" in the kubelet's seccomp directory) and AppArmor profiles.
	FuseResource = corev1.ResourceName("syeyoung.dev/fuse")
	FuseProfile  = "containerservice-fuse"
)

// A StatefulSet's name goes into its pods' controller-revision-hash label (name + "-" + 10
// characters, at most 63), so names stop at 52.
var (
	nameRE   = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$`)
	volumeRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	imageRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
)

// ValidName reports whether name can be a container's name.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Check refuses specs that can't become a container.
func (sp *Spec) Check() error {
	if !nameRE.MatchString(sp.Name) {
		return refuse(http.StatusBadRequest, "A name is 1–52 lowercase letters, digits and dashes, starting with a letter and not ending with a dash.")
	}
	if len(sp.Image) > maxImage || !imageRE.MatchString(sp.Image) {
		return refuse(http.StatusBadRequest, "That isn't an image reference (like docker.io/library/ubuntu:24.04).")
	}
	if sp.CPUMillis < 0 || sp.MemoryMiB < 0 || (sp.MemoryMiB > 0 && sp.MemoryMiB < 64) {
		return refuse(http.StatusBadRequest, "Limits are 0 (none) or positive; memory at least 64 MiB.")
	}
	if sp.DiskGiB == 0 {
		sp.DiskGiB = DefaultDiskGiB
	}
	if sp.DiskGiB < 1 || sp.DiskGiB > MaxDiskGiB {
		return refuse(http.StatusBadRequest, "The disk limit is 1–%d GiB.", MaxDiskGiB)
	}
	if sp.Identity == "" {
		return refuse(http.StatusBadRequest, "A container wears an identity: a new one (newIdentity) or one of yours.")
	}
	if !nameRE.MatchString(sp.Identity) {
		return refuse(http.StatusBadRequest, "An identity's name is 1–52 lowercase letters, digits and dashes, starting with a letter.")
	}
	// Debugging lifts the seccomp filter, and with it the block on the new mount API (fsopen,
	// open_tree, ...), which AppArmor doesn't see: with SYS_ADMIN that would be any mount, in the
	// namespace the terminal joins. One or the other.
	if sp.Debug && sp.FuseMounts {
		return refuse(http.StatusBadRequest, "Debugging and FUSE mounts can't be on together: without the seccomp filter, FUSE's SYS_ADMIN would allow any mount.")
	}
	if len(sp.Mounts) > maxMounts {
		return refuse(http.StatusBadRequest, "At most %d mounts.", maxMounts)
	}
	seen, newNames := map[string]bool{}, map[string]bool{}
	for i := range sp.Mounts {
		m := &sp.Mounts[i]
		if !strings.HasPrefix(m.Path, "/") || path.Clean(m.Path) != m.Path || m.Path == "/" || len(m.Path) > 256 || strings.ContainsAny(m.Path, ":\x00\n") {
			return refuse(http.StatusBadRequest, "Mount path %q: an absolute, clean path other than /.", m.Path)
		}
		for _, reserved := range []string{KeysPath, HostKeyPath, ClaudeLoginPath, "/etc/claude-code"} {
			if m.Path == reserved || strings.HasPrefix(m.Path, reserved+"/") || strings.HasPrefix(reserved, m.Path+"/") {
				return refuse(http.StatusBadRequest, "Mount path %q overlaps %s, where the SSH keys go.", m.Path, reserved)
			}
		}
		if seen[m.Path] {
			return refuse(http.StatusBadRequest, "Mount path %q is listed twice.", m.Path)
		}
		seen[m.Path] = true
		switch {
		case m.New != nil:
			if m.Temp || !volumeRE.MatchString(m.Volume) || m.ReadOnly || m.SizeMiB != 0 {
				return refuse(http.StatusBadRequest, "New volume at %q: a volume name (1–63 lowercase letters, digits and dashes), not read-only.", m.Path)
			}
			if m.New.SizeGiB < 1 || m.New.SizeGiB > maxVolumeGiB {
				return refuse(http.StatusBadRequest, "New volume %q: 1–%d GiB.", m.Volume, maxVolumeGiB)
			}
			if newNames[m.Volume] {
				return refuse(http.StatusBadRequest, "New volume %q is listed twice.", m.Volume)
			}
			newNames[m.Volume] = true
		case m.Temp && m.Volume == "":
			if m.SizeMiB < 0 || m.ReadOnly {
				return refuse(http.StatusBadRequest, "Temporary folder %q: size 0 (no cap) or positive, and not read-only.", m.Path)
			}
		case !m.Temp && volumeRE.MatchString(m.Volume):
			if m.SizeMiB != 0 {
				return refuse(http.StatusBadRequest, "Mount %q: a volume's size is the volume's own.", m.Path)
			}
		default:
			return refuse(http.StatusBadRequest, "Mount %q: give either a volume name or temp.", m.Path)
		}
	}
	return nil
}

// statefulSet is the object a spec becomes; serviceAccount is its identity's ("": none).
func (s *Service) statefulSet(sp Spec, serviceAccount string) *appsv1.StatefulSet {
	podLabels := map[string]string{ManagedBy: managedByValue, NameLabel: sp.Name}
	annotations := map[string]string{}
	if sp.Identity != "" {
		annotations[IdentityAnnotation] = sp.Identity
	}
	if sp.Debug {
		podLabels[DebugLabel] = "true"
	}
	volumes := []corev1.Volume{{Name: "authorized-keys", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: KeysSecret, DefaultMode: ptr.To[int32](0o644), Optional: ptr.To(true)}}}}
	// The pod waits for its host key Secret, made right after the StatefulSet.
	volumes = append(volumes, corev1.Volume{Name: "host-key", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: HostKeySecret(sp.Name), DefaultMode: ptr.To[int32](0o400)}}})
	mounts := []corev1.VolumeMount{{Name: "authorized-keys", MountPath: KeysPath, ReadOnly: true}, {Name: "host-key", MountPath: HostKeyPath, ReadOnly: true}}
	volumes = append(volumes, corev1.Volume{Name: "claude-settings", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: ClaudeSettingsConfigMap}, DefaultMode: ptr.To[int32](0o644), Optional: ptr.To(true)}}})
	mounts = append(mounts, corev1.VolumeMount{Name: "claude-settings", MountPath: ClaudeSettingsPath, ReadOnly: true})
	if sp.ClaudeLogin {
		volumes = append(volumes, corev1.Volume{Name: "claude-login", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: ClaudeLoginSecret, DefaultMode: ptr.To[int32](0o644), Optional: ptr.To(true)}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "claude-login", MountPath: ClaudeLoginPath, ReadOnly: true})
	}
	for i, m := range sp.Mounts {
		name := fmt.Sprintf("m%d", i)
		var src corev1.VolumeSource
		if m.Temp {
			src.EmptyDir = &corev1.EmptyDirVolumeSource{}
			if m.SizeMiB > 0 {
				src.EmptyDir.SizeLimit = resource.NewQuantity(int64(m.SizeMiB)<<20, resource.BinarySI)
			}
		} else {
			src.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: m.Volume}
		}
		volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: src})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: m.Path, ReadOnly: m.ReadOnly})
	}
	// Limits cap; requests are what the scheduler sets aside, kept small (left unset, they'd copy
	// the limits, and a few containers would fill the node on paper).
	limits, requests := corev1.ResourceList{}, corev1.ResourceList{}
	if sp.CPUMillis > 0 {
		limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(sp.CPUMillis), resource.DecimalSI)
		requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(min(sp.CPUMillis, 100)), resource.DecimalSI)
	}
	if sp.MemoryMiB > 0 {
		limits[corev1.ResourceMemory] = *resource.NewQuantity(int64(sp.MemoryMiB)<<20, resource.BinarySI)
		requests[corev1.ResourceMemory] = *resource.NewQuantity(int64(min(sp.MemoryMiB, 256))<<20, resource.BinarySI)
	}
	if sp.DiskGiB > 0 {
		limits[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(sp.DiskGiB)<<30, resource.BinarySI)
		requests[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(1<<30, resource.BinarySI)
	}
	// Hardened: the runtime's default seccomp profile (no nested user namespaces, ptrace, bpf,
	// ...), no raw sockets or device nodes. Debugging: ptrace, and personality(ADDR_NO_RANDOMIZE),
	// which the default profile refuses. Never privileged.
	security := &corev1.SecurityContext{
		Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW", "MKNOD"}},
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if sp.Debug {
		security = &corev1.SecurityContext{
			Capabilities:   &corev1.Capabilities{Add: []corev1.Capability{"SYS_PTRACE"}},
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
		}
	}
	// FUSE mounts (never with Debugging, see Check): SYS_ADMIN counts in the pod's user namespace
	// only (hostUsers: false); the seccomp profile lets mount/umount through and nothing else
	// SYS_ADMIN would, and AppArmor allows fstype fuse and fuse.* only.
	if sp.FuseMounts {
		security.Capabilities.Add = append(security.Capabilities.Add, "SYS_ADMIN")
		security.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeLocalhost, LocalhostProfile: ptr.To(FuseProfile + ".json")}
		security.AppArmorProfile = &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeLocalhost, LocalhostProfile: ptr.To(FuseProfile)}
		limits[FuseResource] = *resource.NewQuantity(1, resource.DecimalSI)
		requests[FuseResource] = *resource.NewQuantity(1, resource.DecimalSI)
	}
	pod := corev1.PodSpec{
		// Root inside is root of the pod's own user namespace only.
		HostUsers: ptr.To(false),
		// Its identity's service account, whose token can do one thing: mint tokens for that
		// account, for any audience (identity.go). Without an identity, no token at all.
		ServiceAccountName:           serviceAccount,
		AutomountServiceAccountToken: ptr.To(serviceAccount != ""),
		EnableServiceLinks:           ptr.To(false),
		Containers: []corev1.Container{{
			Name:            MainContainer,
			Image:           sp.Image,
			VolumeMounts:    mounts,
			Resources:       corev1.ResourceRequirements{Limits: limits, Requests: requests},
			SecurityContext: security,
		}},
		Volumes: volumes,
	}
	if s.cfg.PullSecret != "" {
		pod.ImagePullSecrets = []corev1.LocalObjectReference{{Name: s.cfg.PullSecret}}
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: sp.Name, Namespace: s.cfg.Namespace, Labels: map[string]string{ManagedBy: managedByValue}},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To[int32](1),
			ServiceName:         ServiceName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{NameLabel: sp.Name}},
			Template:            corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: annotations}, Spec: pod},
		},
	}
}

// specOf reads a spec back from a StatefulSet this service made.
func specOf(set *appsv1.StatefulSet) Spec {
	sp := Spec{Name: set.Name}
	pod := set.Spec.Template.Spec
	var main *corev1.Container
	for i := range pod.Containers {
		if pod.Containers[i].Name == MainContainer {
			main = &pod.Containers[i]
		}
	}
	if main == nil {
		return sp
	}
	sp.Image = main.Image
	if q, ok := main.Resources.Limits[corev1.ResourceCPU]; ok {
		sp.CPUMillis = int(q.MilliValue())
	}
	if q, ok := main.Resources.Limits[corev1.ResourceMemory]; ok {
		sp.MemoryMiB = int(q.Value() >> 20)
	}
	if q, ok := main.Resources.Limits[corev1.ResourceEphemeralStorage]; ok {
		sp.DiskGiB = int(q.Value() >> 30)
	}
	sp.Debug = set.Spec.Template.Labels[DebugLabel] == "true"
	_, sp.FuseMounts = main.Resources.Limits[FuseResource]
	sp.Identity = set.Spec.Template.Annotations[IdentityAnnotation]
	vols := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		vols[v.Name] = v
	}
	sp.Mounts = []Mount{}
	for _, vm := range main.VolumeMounts {
		v, ok := vols[vm.Name]
		switch {
		case vm.MountPath == ClaudeLoginPath:
			sp.ClaudeLogin = true
		case !ok || vm.MountPath == KeysPath || vm.MountPath == HostKeyPath || vm.MountPath == ClaudeSettingsPath:
		case v.PersistentVolumeClaim != nil:
			sp.Mounts = append(sp.Mounts, Mount{Path: vm.MountPath, Volume: v.PersistentVolumeClaim.ClaimName, ReadOnly: vm.ReadOnly})
		case v.EmptyDir != nil:
			m := Mount{Path: vm.MountPath, Temp: true}
			if v.EmptyDir.SizeLimit != nil {
				m.SizeMiB = int(v.EmptyDir.SizeLimit.Value() >> 20)
			}
			sp.Mounts = append(sp.Mounts, m)
		}
	}
	return sp
}

// Host is the DNS name of a container's pod.
func (s *Service) Host(name string) string {
	return fmt.Sprintf("%s-0.%s.%s.svc", name, ServiceName, s.cfg.Namespace)
}

func (s *Service) view(set *appsv1.StatefulSet) Container {
	c := Container{Spec: specOf(set), Running: ptr.Deref(set.Spec.Replicas, 1) > 0, Host: s.Host(set.Name), CreatedAt: set.CreationTimestamp.UTC()}
	if c.Identity != "" {
		c.Subject = s.Subject(set.Spec.Template.Spec.ServiceAccountName)
	}
	pod, err := s.pods.Get(set.Name + "-0")
	switch {
	case err != nil && c.Running:
		c.State = "pending"
	case err != nil:
		c.State = "stopped"
	case pod.DeletionTimestamp != nil:
		c.State = "stopping"
	default:
		c.IP = pod.Status.PodIP
		c.State = strings.ToLower(string(pod.Status.Phase))
		for _, st := range pod.Status.ContainerStatuses {
			if st.Name != MainContainer {
				continue
			}
			c.Restarts = st.RestartCount
			if w := st.State.Waiting; w != nil {
				c.State, c.Message = w.Reason, w.Message
			} else if st.State.Running != nil {
				c.State = "running"
			}
		}
		if c.State == "pending" {
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
					c.Message = cond.Message
				}
			}
		}
	}
	return c
}

// List is every container, by name.
func (s *Service) List() ([]Container, error) {
	sets, err := s.sets.List(managed())
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(sets))
	for _, set := range sets {
		out = append(out, s.view(set))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get is the container named name.
func (s *Service) Get(name string) (Container, error) {
	set, err := s.sets.Get(name)
	if err != nil || set.Labels[ManagedBy] != managedByValue {
		return Container{}, refuse(http.StatusNotFound, "There is no container %q.", name)
	}
	return s.view(set), nil
}

// prepareMounts checks a spec's volumes (an existing one must be there, a new one must not, and
// its class must make volumes) and then makes the new ones. Everything is checked before anything
// is made; should a step fail after that, the volumes made stay, as volumes.
func (s *Service) prepareMounts(ctx context.Context, sp Spec) error {
	for _, m := range sp.Mounts {
		_, err := s.vols.Get(m.Volume)
		switch {
		case m.Temp:
		case m.New == nil && err != nil:
			return refuse(http.StatusBadRequest, "There is no volume %q.", m.Volume)
		case m.New != nil && err == nil:
			return refuse(http.StatusConflict, "A volume named %q already exists.", m.Volume)
		case m.New != nil:
			if err := s.checkClass(ctx, m.New.StorageClass); err != nil {
				return err
			}
		}
	}
	for _, m := range sp.Mounts {
		if m.New != nil {
			if _, err := s.CreateVolume(ctx, m.Volume, m.New.StorageClass, m.New.SizeGiB); err != nil {
				return err
			}
		}
	}
	return nil
}

// Create makes the container and starts it, making its new volumes first, then its host key and
// its identity (service account). The name is claimed by the API server: creating a StatefulSet
// that exists fails.
func (s *Service) Create(ctx context.Context, sp Spec) (Container, error) {
	if err := sp.Check(); err != nil {
		return Container{}, err
	}
	if _, err := s.sets.Get(sp.Name); err == nil {
		return Container{}, refuse(http.StatusConflict, "A container named %q already exists.", sp.Name)
	}
	if err := s.checkIdentity(ctx, sp); err != nil {
		return Container{}, err
	}
	if err := s.prepareMounts(ctx, sp); err != nil {
		return Container{}, err
	}
	sa, err := s.attachIdentity(ctx, sp)
	if err != nil {
		return Container{}, err
	}
	set, err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Create(ctx, s.statefulSet(sp, sa), metav1.CreateOptions{})
	if err != nil {
		return Container{}, kubeError(err, fmt.Sprintf("A container named %q", sp.Name))
	}
	// Without its host key the pod never starts: take the container back then.
	if err := s.createHostKey(ctx, set); err != nil {
		return Container{}, s.rollback(ctx, set, err)
	}
	return s.view(set), nil
}

func (s *Service) rollback(ctx context.Context, set *appsv1.StatefulSet, err error) error {
	derr := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Delete(ctx, set.Name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	return errors.Join(err, derr)
}

// Update changes a container's settings to sp (its name stays): new volumes are made first, then
// the pod template is replaced, which restarts a running container's pod (volumes keep their data;
// what ran in it is gone). A stopped container stays stopped. Settings are the whole spec, checked
// as for Create.
func (s *Service) Update(ctx context.Context, name string, sp Spec) (Container, error) {
	if sp.Name != name {
		return Container{}, refuse(http.StatusBadRequest, "A container's name can't change.")
	}
	if err := sp.Check(); err != nil {
		return Container{}, err
	}
	cur, err := s.sets.Get(name)
	if err != nil || cur.Labels[ManagedBy] != managedByValue {
		return Container{}, refuse(http.StatusNotFound, "There is no container %q.", name)
	}
	if err := s.checkIdentity(ctx, sp); err != nil {
		return Container{}, err
	}
	if err := s.prepareMounts(ctx, sp); err != nil {
		return Container{}, err
	}
	sa, err := s.attachIdentity(ctx, sp)
	if err != nil {
		return Container{}, err
	}
	next := cur.DeepCopy()
	next.Spec.Template = s.statefulSet(sp, sa).Spec.Template
	set, err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Update(ctx, next, metav1.UpdateOptions{})
	if err != nil {
		return Container{}, kubeError(err, fmt.Sprintf("The container %q", name))
	}
	return s.view(set), nil
}

// SetRunning starts (1 replica) or stops (0) the container.
func (s *Service) SetRunning(ctx context.Context, name string, running bool) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	replicas := 0
	if running {
		replicas = 1
	}
	patch := fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, replicas)
	_, err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return kubeError(err, fmt.Sprintf("The container %q", name))
}

// Delete removes the container; its pod goes with it. Volumes stay.
func (s *Service) Delete(ctx context.Context, name string, withIdentity bool) error {
	c, err := s.Get(name)
	if err != nil {
		return err
	}
	err = s.client.AppsV1().StatefulSets(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	if err != nil {
		return kubeError(err, fmt.Sprintf("The container %q", name))
	}
	// Its identity too, if asked and nothing else wears it (DeleteIdentity refuses then).
	if withIdentity && c.Identity != "" {
		return s.DeleteIdentity(ctx, c.Identity, name)
	}
	return nil
}

// checkIdentity refuses a spec whose identity is wrong before anything is made: a new one whose
// name is taken, or an existing one that isn't there.
func (s *Service) checkIdentity(ctx context.Context, sp Spec) error {
	if sp.Identity == "" {
		return nil
	}
	_, err := s.keys.Identity(ctx, sp.Identity)
	switch {
	case sp.NewIdentity && err == nil:
		return refuse(http.StatusConflict, "An identity named %q exists already.", sp.Identity)
	case sp.NewIdentity && errors.Is(err, store.ErrNotFound):
		return nil
	case errors.Is(err, store.ErrNotFound):
		return refuse(http.StatusBadRequest, "There is no identity %q.", sp.Identity)
	}
	return err
}

// attachIdentity is the service account of the spec's identity, made first if it's new.
func (s *Service) attachIdentity(ctx context.Context, sp Spec) (string, error) {
	if sp.Identity == "" {
		return "", nil
	}
	if sp.NewIdentity {
		id, err := s.CreateIdentity(ctx, sp.Identity)
		return id.ServiceAccount, err
	}
	id, err := s.identity(ctx, sp.Identity)
	return id.ServiceAccount, err
}

// Pod is the pod a container's terminal execs into.
func (s *Service) Pod(name string) (string, error) {
	c, err := s.Get(name)
	if err != nil {
		return "", err
	}
	if c.State != "running" {
		return "", refuse(http.StatusConflict, "The container %q isn't running (%s).", name, c.State)
	}
	return name + "-0", nil
}
