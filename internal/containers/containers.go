package containers

import (
	"context"
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
}

// Mount is a volume (a PVC by name) or a temporary folder (Temp) at Path.
type Mount struct {
	Path     string `json:"path"`
	Volume   string `json:"volume,omitempty"`
	Temp     bool   `json:"temp,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// SizeMiB caps a temporary folder; 0: no cap.
	SizeMiB int `json:"sizeMiB,omitempty"`
}

// Container is a container as the API shows it.
type Container struct {
	Spec
	// Running: it should be (1 replica); the pod's own state is State.
	Running bool `json:"running"`
	// State is "stopped", "stopping", "pending", "running", or why the container is waiting
	// (ImagePullBackOff, CrashLoopBackOff, ...).
	State     string    `json:"state"`
	Message   string    `json:"message,omitempty"`
	Restarts  int32     `json:"restarts"`
	Host      string    `json:"host"` // the pod's DNS name, what the bastion connects to
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

const (
	maxMounts = 16
	maxImage  = 512
)

// A StatefulSet's name goes into its pods' controller-revision-hash label (name + "-" + 10
// characters, at most 63), so names stop at 52.
var (
	nameRE   = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$`)
	volumeRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	imageRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
)

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
	if len(sp.Mounts) > maxMounts {
		return refuse(http.StatusBadRequest, "At most %d mounts.", maxMounts)
	}
	seen := map[string]bool{}
	for i := range sp.Mounts {
		m := &sp.Mounts[i]
		if !strings.HasPrefix(m.Path, "/") || path.Clean(m.Path) != m.Path || m.Path == "/" || len(m.Path) > 256 || strings.ContainsAny(m.Path, ":\x00\n") {
			return refuse(http.StatusBadRequest, "Mount path %q: an absolute, clean path other than /.", m.Path)
		}
		if m.Path == KeysPath || strings.HasPrefix(m.Path+"/", KeysPath+"/") || strings.HasPrefix(KeysPath, m.Path+"/") {
			return refuse(http.StatusBadRequest, "Mount path %q overlaps %s, where the SSH keys go.", m.Path, KeysPath)
		}
		if seen[m.Path] {
			return refuse(http.StatusBadRequest, "Mount path %q is listed twice.", m.Path)
		}
		seen[m.Path] = true
		switch {
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

// statefulSet is the object a spec becomes.
func (s *Service) statefulSet(sp Spec) *appsv1.StatefulSet {
	podLabels := map[string]string{ManagedBy: managedByValue, NameLabel: sp.Name}
	volumes := []corev1.Volume{{Name: "authorized-keys", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName: KeysSecret, DefaultMode: ptr.To[int32](0o644), Optional: ptr.To(true)}}}}
	mounts := []corev1.VolumeMount{{Name: "authorized-keys", MountPath: KeysPath, ReadOnly: true}}
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
	limits := corev1.ResourceList{}
	if sp.CPUMillis > 0 {
		limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(sp.CPUMillis), resource.DecimalSI)
	}
	if sp.MemoryMiB > 0 {
		limits[corev1.ResourceMemory] = *resource.NewQuantity(int64(sp.MemoryMiB)<<20, resource.BinarySI)
	}
	pod := corev1.PodSpec{
		// Root inside is root of the pod's own user namespace only.
		HostUsers:                    ptr.To(false),
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		Containers: []corev1.Container{{
			Name:         MainContainer,
			Image:        sp.Image,
			VolumeMounts: mounts,
			Resources:    corev1.ResourceRequirements{Limits: limits},
			SecurityContext: &corev1.SecurityContext{
				// Debuggers: ptrace, and personality(ADDR_NO_RANDOMIZE), which the runtime's default
				// seccomp profile refuses. Never privileged.
				Capabilities:   &corev1.Capabilities{Add: []corev1.Capability{"SYS_PTRACE"}},
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
			},
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
			Template:            corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: pod},
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
	vols := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		vols[v.Name] = v
	}
	sp.Mounts = []Mount{}
	for _, vm := range main.VolumeMounts {
		v, ok := vols[vm.Name]
		switch {
		case !ok || vm.MountPath == KeysPath:
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

// Create makes the container and starts it. The name is claimed by the API server: creating a
// StatefulSet that exists fails.
func (s *Service) Create(ctx context.Context, sp Spec) (Container, error) {
	if err := sp.Check(); err != nil {
		return Container{}, err
	}
	for _, m := range sp.Mounts {
		if !m.Temp {
			if _, err := s.vols.Get(m.Volume); err != nil {
				return Container{}, refuse(http.StatusBadRequest, "There is no volume %q.", m.Volume)
			}
		}
	}
	set, err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Create(ctx, s.statefulSet(sp), metav1.CreateOptions{})
	if err != nil {
		return Container{}, kubeError(err, fmt.Sprintf("A container named %q", sp.Name))
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
func (s *Service) Delete(ctx context.Context, name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
	return kubeError(err, fmt.Sprintf("The container %q", name))
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
