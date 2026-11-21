package containers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// Volume is a volume as the API shows it.
type Volume struct {
	Name         string `json:"name"`
	StorageClass string `json:"storageClass"`
	// SizeGiB is what was asked for; CapacityGiB what the volume has (0 until it's bound).
	SizeGiB     int       `json:"sizeGiB"`
	CapacityGiB int       `json:"capacityGiB"`
	Phase       string    `json:"phase"` // pending, bound, lost, deleting
	UsedBy      []string  `json:"usedBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// StorageClass is a class a volume can be made in.
type StorageClass struct {
	Name       string `json:"name"`
	Default    bool   `json:"default"`
	Expandable bool   `json:"expandable"`
}

const (
	maxVolumeGiB  = 16 << 10
	noProvisioner = "kubernetes.io/no-provisioner"
)

func gib(q resource.Quantity) int { return int(q.Value() >> 30) }

// usedBy is which containers mount each volume.
func (s *Service) usedBy() (map[string][]string, error) {
	sets, err := s.sets.List(managed())
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, set := range sets {
		for _, v := range set.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				out[v.PersistentVolumeClaim.ClaimName] = append(out[v.PersistentVolumeClaim.ClaimName], set.Name)
			}
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

func volumeView(pvc *corev1.PersistentVolumeClaim, used []string) Volume {
	v := Volume{Name: pvc.Name, StorageClass: ptr.Deref(pvc.Spec.StorageClassName, ""), Phase: "pending", UsedBy: used, CreatedAt: pvc.CreationTimestamp.UTC()}
	if v.UsedBy == nil {
		v.UsedBy = []string{}
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		v.SizeGiB = gib(q)
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		v.CapacityGiB = gib(q)
	}
	switch {
	case pvc.DeletionTimestamp != nil:
		v.Phase = "deleting"
	case pvc.Status.Phase == corev1.ClaimBound:
		v.Phase = "bound"
	case pvc.Status.Phase == corev1.ClaimLost:
		v.Phase = "lost"
	}
	return v
}

// Volumes is every volume, by name.
func (s *Service) Volumes() ([]Volume, error) {
	pvcs, err := s.vols.List(managed())
	if err != nil {
		return nil, err
	}
	used, err := s.usedBy()
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(pvcs))
	for _, pvc := range pvcs {
		out = append(out, volumeView(pvc, used[pvc.Name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// StorageClasses is the cluster's storage classes that make volumes on demand: one without a
// provisioner ("kubernetes.io/no-provisioner", static PVs only) would leave a new volume pending for
// good. Read from the API server when asked: it's a form's list, not a hot path.
func (s *Service) StorageClasses(ctx context.Context) ([]StorageClass, error) {
	list, err := s.client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]StorageClass, 0, len(list.Items))
	for _, c := range list.Items {
		if c.Provisioner == noProvisioner {
			continue
		}
		out = append(out, StorageClass{
			Name:       c.Name,
			Default:    c.Annotations["storageclass.kubernetes.io/is-default-class"] == "true",
			Expandable: ptr.Deref(c.AllowVolumeExpansion, false),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateVolume makes a volume of sizeGiB in class (empty: the cluster's default class).
func (s *Service) CreateVolume(ctx context.Context, name, class string, sizeGiB int) (Volume, error) {
	if !volumeRE.MatchString(name) {
		return Volume{}, refuse(http.StatusBadRequest, "A volume name is 1–63 lowercase letters, digits and dashes, not starting or ending with a dash.")
	}
	if sizeGiB < 1 || sizeGiB > maxVolumeGiB {
		return Volume{}, refuse(http.StatusBadRequest, "A volume is 1–%d GiB.", maxVolumeGiB)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace, Labels: map[string]string{ManagedBy: managedByValue}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *resource.NewQuantity(int64(sizeGiB)<<30, resource.BinarySI)}},
		},
	}
	if err := s.checkClass(ctx, class); err != nil {
		return Volume{}, err
	}
	if class != "" {
		pvc.Spec.StorageClassName = &class
	}
	pvc, err := s.client.CoreV1().PersistentVolumeClaims(s.cfg.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return Volume{}, kubeError(err, fmt.Sprintf("A volume named %q", name))
	}
	return volumeView(pvc, nil), nil
}

// checkClass refuses a class that doesn't exist or makes no volumes; empty is the default class.
func (s *Service) checkClass(ctx context.Context, class string) error {
	if class == "" {
		return nil
	}
	sc, err := s.client.StorageV1().StorageClasses().Get(ctx, class, metav1.GetOptions{})
	if err != nil || sc.Provisioner == noProvisioner {
		return refuse(http.StatusBadRequest, "There is no storage class %q that makes volumes.", class)
	}
	return nil
}

func (s *Service) volume(name string) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := s.vols.Get(name)
	if err != nil || pvc.Labels[ManagedBy] != managedByValue {
		return nil, refuse(http.StatusNotFound, "There is no volume %q.", name)
	}
	return pvc, nil
}

// ResizeVolume grows a volume to sizeGiB; whether it can is the storage class's (and Kubernetes')
// to say.
func (s *Service) ResizeVolume(ctx context.Context, name string, sizeGiB int) error {
	pvc, err := s.volume(name)
	if err != nil {
		return err
	}
	if sizeGiB > maxVolumeGiB {
		return refuse(http.StatusBadRequest, "A volume is at most %d GiB.", maxVolumeGiB)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; sizeGiB <= gib(q) {
		return refuse(http.StatusBadRequest, "Volumes only grow: %q is %d GiB already.", name, gib(q))
	}
	patch := fmt.Appendf(nil, `{"spec":{"resources":{"requests":{"storage":"%dGi"}}}}`, sizeGiB)
	_, err = s.client.CoreV1().PersistentVolumeClaims(s.cfg.Namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return kubeError(err, fmt.Sprintf("The volume %q", name))
}

// DeleteVolume removes a volume no container mounts. (Should one be created meanwhile with it,
// Kubernetes' PVC protection keeps the volume until that pod is gone.)
func (s *Service) DeleteVolume(ctx context.Context, name string) error {
	if _, err := s.volume(name); err != nil {
		return err
	}
	used, err := s.usedBy()
	if err != nil {
		return err
	}
	if len(used[name]) > 0 {
		return refuse(http.StatusConflict, "The volume %q is mounted by %v; delete those containers first.", name, used[name])
	}
	err = s.client.CoreV1().PersistentVolumeClaims(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	return kubeError(err, fmt.Sprintf("The volume %q", name))
}
