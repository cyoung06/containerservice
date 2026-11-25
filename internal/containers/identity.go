package containers

// Identities: what a container is to other services. An identity is a service account of the
// namespace, made by the owner (or, by default, with a new container) and worn by the containers
// it is attached to, like an IAM role on an instance. Its account's mounted token may do one thing:
// create tokens for that account (TokenRequest), for any audience (the images' sa-token). The
// tokens' subject for the cluster's issuer, system:serviceaccount:<namespace>:<account>, is what
// other services grant to (the drive's workload grants). The account's name has a random part, so
// a subject is never used again: an identity deleted and made again under the same name is
// another one, and grants to the old one, and tokens it minted, match nothing of the new one's.
//
// The record of identities is the database (names and accounts); Kubernetes holds the accounts and
// their Role and RoleBinding. A container's identity is its pod template's IdentityAnnotation.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cyoung06/containerservice/internal/store"
)

const (
	// IdentityAnnotation on a container's pod template names its identity.
	IdentityAnnotation = "containerservice.syeyoung.dev/identity"
	// IdentityLabel on an identity's account, Role and RoleBinding names the identity.
	IdentityLabel = "containerservice.syeyoung.dev/identity"
	// MaxIdentities bounds how many there may be.
	MaxIdentities = 100
)

// Identity is an identity as the API shows it.
type Identity struct {
	Name           string    `json:"name"`
	ServiceAccount string    `json:"serviceAccount"`
	Subject        string    `json:"subject"`
	UsedBy         []string  `json:"usedBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

func newServiceAccountName(identity string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "identity-" + identity + "-" + string(b)
}

// Subject is the identity a service account's tokens carry for the cluster's issuer.
func (s *Service) Subject(serviceAccount string) string {
	if serviceAccount == "" {
		return ""
	}
	return "system:serviceaccount:" + s.cfg.Namespace + ":" + serviceAccount
}

// identityUsers is which containers wear each identity.
func (s *Service) identityUsers() (map[string][]string, error) {
	sets, err := s.sets.List(managed())
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, set := range sets {
		if id := set.Spec.Template.Annotations[IdentityAnnotation]; id != "" {
			out[id] = append(out[id], set.Name)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

// Identities is every identity, by name.
func (s *Service) Identities(ctx context.Context) ([]Identity, error) {
	ids, err := s.keys.Identities(ctx)
	if err != nil {
		return nil, err
	}
	used, err := s.identityUsers()
	if err != nil {
		return nil, err
	}
	out := make([]Identity, 0, len(ids))
	for _, id := range ids {
		users := used[id.Name]
		if users == nil {
			users = []string{}
		}
		out = append(out, Identity{Name: id.Name, ServiceAccount: id.ServiceAccount, Subject: s.Subject(id.ServiceAccount), UsedBy: users, CreatedAt: id.CreatedAt.UTC()})
	}
	return out, nil
}

// CreateIdentity makes an identity: its record, then its account, Role and RoleBinding.
func (s *Service) CreateIdentity(ctx context.Context, name string) (Identity, error) {
	if !nameRE.MatchString(name) {
		return Identity{}, refuse(http.StatusBadRequest, "An identity's name is 1–52 lowercase letters, digits and dashes, starting with a letter and not ending with a dash.")
	}
	id := store.Identity{Name: name, ServiceAccount: newServiceAccountName(name)}
	added, err := s.keys.AddIdentity(ctx, id, MaxIdentities)
	switch {
	case errors.Is(err, store.ErrExists):
		return Identity{}, refuse(http.StatusConflict, "An identity named %q exists already.", name)
	case err != nil:
		return Identity{}, err
	case !added:
		return Identity{}, refuse(http.StatusConflict, "At most %d identities.", MaxIdentities)
	}
	// Should this fail, the record stays and the objects are made when the identity is attached.
	if err := s.ensureIdentityObjects(ctx, id); err != nil {
		return Identity{}, err
	}
	return Identity{Name: name, ServiceAccount: id.ServiceAccount, Subject: s.Subject(id.ServiceAccount), UsedBy: []string{}, CreatedAt: time.Now().UTC()}, nil
}

// identity is the identity called name (404 if there is none), its objects made if missing.
func (s *Service) identity(ctx context.Context, name string) (store.Identity, error) {
	id, err := s.keys.Identity(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return id, refuse(http.StatusBadRequest, "There is no identity %q.", name)
	}
	if err != nil {
		return id, err
	}
	if err := s.ensureIdentityObjects(ctx, id); err != nil {
		return id, err
	}
	// Deleted meanwhile (its record goes first): undo what was just made rather than leave an
	// account nobody has a record of.
	if _, err := s.keys.Identity(ctx, name); errors.Is(err, store.ErrNotFound) {
		return id, errors.Join(refuse(http.StatusConflict, "The identity %q was deleted meanwhile.", name), s.deleteIdentityObjects(ctx, id.ServiceAccount))
	}
	return id, nil
}

// ensureIdentityObjects makes an identity's account, and the Role (with its binding) that lets the
// account's token create tokens for that account and nothing else. Ones that exist are its own:
// the account's name is the identity's alone.
func (s *Service) ensureIdentityObjects(ctx context.Context, id store.Identity) error {
	sa := id.ServiceAccount
	meta := metav1.ObjectMeta{Name: sa, Namespace: s.cfg.Namespace, Labels: map[string]string{ManagedBy: managedByValue, IdentityLabel: id.Name}}
	created := func(err error, what string) error {
		if err == nil || apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating the %s %s: %w", what, sa, err)
	}
	_, err := s.client.CoreV1().ServiceAccounts(s.cfg.Namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: meta}, metav1.CreateOptions{})
	if err := created(err, "service account"); err != nil {
		return err
	}
	_, err = s.client.RbacV1().Roles(s.cfg.Namespace).Create(ctx, &rbacv1.Role{
		ObjectMeta: meta,
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"serviceaccounts/token"}, ResourceNames: []string{sa}, Verbs: []string{"create"}}},
	}, metav1.CreateOptions{})
	if err := created(err, "role"); err != nil {
		return err
	}
	_, err = s.client.RbacV1().RoleBindings(s.cfg.Namespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: meta,
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: sa},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: s.cfg.Namespace}},
	}, metav1.CreateOptions{})
	return created(err, "role binding")
}

// DeleteIdentity removes an identity no container wears (but except, the container being deleted
// with it). Who wears it is read from the API server, not the cache, so a container made a moment
// ago counts. The record goes first (an attach that comes meanwhile then finds no identity, and
// undoes what it made: attachIdentity), then the binding, Role and account. Tokens it minted stop
// being accepted by the API server at once; a service that checks them by the issuer's keys only
// (the drive) accepts them until they expire (at most a day), but nothing new matches its subject.
func (s *Service) DeleteIdentity(ctx context.Context, name, except string) error {
	if _, err := s.keys.Identity(ctx, name); errors.Is(err, store.ErrNotFound) {
		return refuse(http.StatusNotFound, "There is no identity %q.", name)
	} else if err != nil {
		return err
	}
	sets, err := s.client.AppsV1().StatefulSets(s.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: ManagedBy + "=" + managedByValue})
	if err != nil {
		return err
	}
	var wearers []string
	for _, set := range sets.Items {
		if set.Spec.Template.Annotations[IdentityAnnotation] == name && set.Name != except && set.DeletionTimestamp == nil {
			wearers = append(wearers, set.Name)
		}
	}
	if len(wearers) > 0 {
		sort.Strings(wearers)
		return refuse(http.StatusConflict, "The identity %q is worn by %v; change or delete those containers first.", name, wearers)
	}
	id, err := s.keys.DeleteIdentity(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return refuse(http.StatusNotFound, "There is no identity %q.", name)
	}
	if err != nil {
		return err
	}
	return s.deleteIdentityObjects(ctx, id.ServiceAccount)
}

func (s *Service) deleteIdentityObjects(ctx context.Context, sa string) error {
	gone := func(err error) error {
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := gone(s.client.RbacV1().RoleBindings(s.cfg.Namespace).Delete(ctx, sa, metav1.DeleteOptions{})); err != nil {
		return err
	}
	if err := gone(s.client.RbacV1().Roles(s.cfg.Namespace).Delete(ctx, sa, metav1.DeleteOptions{})); err != nil {
		return err
	}
	return gone(s.client.CoreV1().ServiceAccounts(s.cfg.Namespace).Delete(ctx, sa, metav1.DeleteOptions{}))
}
