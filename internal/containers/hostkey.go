package containers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"golang.org/x/crypto/ssh"
)

// HostKeySecret is the Secret holding a container's SSH host key.
func HostKeySecret(name string) string { return name + "-hostkey" }

// createHostKey makes the container's host key Secret, owned by its StatefulSet so it goes when the
// container does (the garbage collector deletes it; this service never needs to).
func (s *Service) createHostKey(ctx context.Context, set *appsv1.StatefulSet) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: HostKeySecret(set.Name), Namespace: s.cfg.Namespace,
			Labels:          map[string]string{ManagedBy: managedByValue, NameLabel: set.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: set.Name, UID: set.UID}},
		},
		Data: map[string][]byte{
			"ssh_host_ed25519_key":     pem.EncodeToMemory(block),
			"ssh_host_ed25519_key.pub": ssh.MarshalAuthorizedKey(sshPub),
		},
	}
	_, err = s.client.CoreV1().Secrets(s.cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// The last container of this name was deleted a moment ago and its key isn't collected yet.
		return refuse(http.StatusConflict, "A container named %q was just deleted; try again in a moment.", set.Name)
	}
	if err != nil {
		return fmt.Errorf("creating the host key: %w", err)
	}
	return nil
}
