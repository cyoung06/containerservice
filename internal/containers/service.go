// Package containers is the owner's containers and volumes, as Kubernetes objects in one namespace:
//
//   - a container is a StatefulSet of one replica (stop: 0 replicas), whose pod is reached by the
//     shared headless Service "containers" at <name>-0.containers.<namespace>.svc;
//   - a volume is a PersistentVolumeClaim;
//   - the SSH keys are copied from the database into the Secret "authorized-keys", which every
//     container mounts at /etc/containerservice.
//
// Kubernetes is the only record of containers and volumes: what exists is what the informers see.
// This package is the only writer of these objects.
package containers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/syeyoung-cloud/containerservice/internal/store"
)

const (
	// ManagedBy labels everything this service makes; the informers see only those.
	ManagedBy      = "app.kubernetes.io/managed-by"
	managedByValue = "containerservice"
	// NameLabel on a pod is its container's name.
	NameLabel = "containerservice.syeyoung.dev/name"
	// ServiceName is the headless Service every container's pod is reached through (deployed with
	// the service, not made here).
	ServiceName = "containers"
	// KeysSecret holds authorized_keys.
	KeysSecret = "authorized-keys"
	// KeysPath is where containers see it: KeysPath/authorized_keys.
	KeysPath = "/etc/containerservice"
	// HostKeyPath is where a container sees its own SSH host key, kept across restarts in the
	// Secret HostKeySecret(name): HostKeyPath/ssh_host_ed25519_key (and .pub).
	HostKeyPath = "/etc/containerservice-host"
	// ClaudeLoginSecret holds the owner's Claude Code token ("token"), mounted at ClaudeLoginPath
	// in containers that ask for it.
	ClaudeLoginSecret = "claude-login"
	ClaudeLoginPath   = "/etc/containerservice-claude"
	// ClaudeSettingsConfigMap holds the shared settings ("console.json"), mounted in every
	// container at ClaudeSettingsPath, Claude Code's managed settings drop-in directory.
	ClaudeSettingsConfigMap = "claude-settings"
	ClaudeSettingsPath      = "/etc/claude-code/managed-settings.d"
)

// Error is a request refused, with the status and message to show.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func refuse(status int, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Config is what the containers get from the deployment.
type Config struct {
	Namespace string
	// PullSecret, if set, is an image pull secret every container's pod lists.
	PullSecret string
}

// Keys is where the SSH keys and the Claude login and settings are read from (the database).
type Keys interface {
	KeyLines(ctx context.Context) ([]string, error)
	// ClaudeConfig is the Claude Code token and settings JSON ("" for none).
	ClaudeConfig(ctx context.Context) (token, settings string, err error)
	// The identities' records (identity.go).
	AddIdentity(ctx context.Context, id store.Identity, max int) (bool, error)
	Identities(ctx context.Context) ([]store.Identity, error)
	Identity(ctx context.Context, name string) (store.Identity, error)
	DeleteIdentity(ctx context.Context, name string) (store.Identity, error)
}

// Service manages the containers and volumes.
type Service struct {
	cfg    Config
	client kubernetes.Interface
	keys   Keys
	log    *slog.Logger

	sets  appslisters.StatefulSetNamespaceLister
	pods  corelisters.PodNamespaceLister
	vols  corelisters.PersistentVolumeClaimNamespaceLister
	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// New starts the informers and waits for their first list.
func New(ctx context.Context, client kubernetes.Interface, cfg Config, keys Keys, log *slog.Logger) (*Service, error) {
	s := &Service{cfg: cfg, client: client, keys: keys, log: log, subs: map[chan struct{}]struct{}{}}
	// Resync 0: no periodic re-lists; every change arrives as a watch event.
	f := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(cfg.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = ManagedBy + "=" + managedByValue }))
	sets := f.Apps().V1().StatefulSets()
	pods := f.Core().V1().Pods()
	vols := f.Core().V1().PersistentVolumeClaims()
	notify := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { s.notify() },
		UpdateFunc: func(any, any) { s.notify() },
		DeleteFunc: func(any) { s.notify() },
	}
	for _, i := range []cache.SharedIndexInformer{sets.Informer(), pods.Informer(), vols.Informer()} {
		if _, err := i.AddEventHandler(notify); err != nil {
			return nil, err
		}
	}
	s.sets = sets.Lister().StatefulSets(cfg.Namespace)
	s.pods = pods.Lister().Pods(cfg.Namespace)
	s.vols = vols.Lister().PersistentVolumeClaims(cfg.Namespace)
	f.Start(ctx.Done())
	for typ, ok := range f.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return nil, fmt.Errorf("the %v informer didn't sync", typ)
		}
	}
	return s, nil
}

// Subscribe returns a channel that receives (coalesced) whenever a container or volume changes,
// and the function that ends the subscription.
func (s *Service) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

func (s *Service) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // one is pending already
		}
	}
}

func managed() labels.Selector {
	return labels.SelectorFromSet(labels.Set{ManagedBy: managedByValue})
}

// kubeError turns the API server's refusals into Errors; anything else is returned as it is.
func kubeError(err error, what string) error {
	var status apierrors.APIStatus
	switch {
	case err == nil:
		return nil
	case apierrors.IsAlreadyExists(err):
		return refuse(http.StatusConflict, "%s already exists.", what)
	case apierrors.IsNotFound(err):
		return refuse(http.StatusNotFound, "%s doesn't exist.", what)
	case apierrors.IsConflict(err):
		return refuse(http.StatusConflict, "%s changed meanwhile; try again.", what)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err), apierrors.IsForbidden(err):
		if errors.As(err, &status) {
			return refuse(http.StatusUnprocessableEntity, "Kubernetes refused it: %s", status.Status().Message)
		}
	}
	return err
}

// Namespace is the namespace of the containers and volumes.
func (s *Service) Namespace() string { return s.cfg.Namespace }
