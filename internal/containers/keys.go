package containers

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const minRSABits = 2048

// KeyID is the id of a public key: hex SHA-256 of its wire form.
func KeyID(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return hex.EncodeToString(sum[:])
}

// ParseKey checks one OpenSSH public key line: ed25519, ECDSA or RSA of at least 2048 bits, and
// no options. It returns the key and the line's comment.
func ParseKey(line string) (ssh.PublicKey, string, error) {
	line = strings.TrimSpace(line)
	if strings.ContainsAny(line, "\r\n") {
		return nil, "", refuse(http.StatusBadRequest, "Paste one key: a single line.")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(rest) > 0 {
		return nil, "", refuse(http.StatusBadRequest, "That isn't an OpenSSH public key line (like \"ssh-ed25519 AAAA… you@laptop\").")
	}
	if len(options) > 0 {
		return nil, "", refuse(http.StatusBadRequest, "Keys can't carry options (like command= or from=).")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
	case ssh.KeyAlgoRSA:
		ck, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return nil, "", refuse(http.StatusBadRequest, "That RSA key couldn't be read.")
		}
		if pk, ok := ck.CryptoPublicKey().(*rsa.PublicKey); !ok || pk.N.BitLen() < minRSABits {
			return nil, "", refuse(http.StatusBadRequest, "RSA keys must be at least %d bits.", minRSABits)
		}
	default:
		return nil, "", refuse(http.StatusBadRequest, "Use an ed25519, ECDSA or RSA key (not %s).", key.Type())
	}
	return key, strings.TrimSpace(comment), nil
}

// SyncKeys writes the database's keys into the Secret, after every change to them and at start.
//
// The Secret is read before the database and written with that read's resourceVersion, so a write
// only lands if nobody wrote since: of two concurrent syncs, one that read the database before the
// other's change was committed can't land after the other. A refused write is done again from a
// fresh read.
func (s *Service) SyncKeys(ctx context.Context) error {
	secrets := s.client.CoreV1().Secrets(s.cfg.Namespace)
	for attempt := 0; ; attempt++ {
		current, err := secrets.Get(ctx, KeysSecret, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		exists := err == nil
		lines, err := s.keys.KeyLines(ctx)
		if err != nil {
			return err
		}
		content := ""
		for _, l := range lines {
			content += l + "\n"
		}
		if exists {
			if string(current.Data["authorized_keys"]) == content {
				return nil
			}
			current.Data = map[string][]byte{"authorized_keys": []byte(content)}
			_, err = secrets.Update(ctx, current, metav1.UpdateOptions{})
		} else {
			_, err = secrets.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: KeysSecret, Namespace: s.cfg.Namespace, Labels: map[string]string{ManagedBy: managedByValue}},
				Data:       map[string][]byte{"authorized_keys": []byte(content)},
			}, metav1.CreateOptions{})
		}
		if (apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)) && attempt < 10 {
			continue
		}
		if err != nil {
			return fmt.Errorf("writing the Secret %s: %w", KeysSecret, err)
		}
		return nil
	}
}
