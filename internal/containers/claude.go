package containers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SyncClaude copies the Claude Code token into the Secret ClaudeLoginSecret and the settings into
// the ConfigMap ClaudeSettingsConfigMap, from the database. Like SyncKeys: idempotent, and redone
// at start should one fail after a change.
func (s *Service) SyncClaude(ctx context.Context) error {
	token, settings, err := s.keys.ClaudeConfig(ctx)
	if err != nil {
		return err
	}
	if settings == "" {
		settings = "{}"
	}
	secrets := s.client.CoreV1().Secrets(s.cfg.Namespace)
	err = upsert(ctx, func() (bool, error) {
		cur, err := secrets.Get(ctx, ClaudeLoginSecret, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: s.meta(ClaudeLoginSecret), Data: map[string][]byte{"token": []byte(token)}}, metav1.CreateOptions{})
			return true, err
		}
		if err != nil || string(cur.Data["token"]) == token {
			return false, err
		}
		cur.Data = map[string][]byte{"token": []byte(token)}
		_, err = secrets.Update(ctx, cur, metav1.UpdateOptions{})
		return true, err
	})
	if err != nil {
		return fmt.Errorf("writing the Secret %s: %w", ClaudeLoginSecret, err)
	}
	maps := s.client.CoreV1().ConfigMaps(s.cfg.Namespace)
	err = upsert(ctx, func() (bool, error) {
		cur, err := maps.Get(ctx, ClaudeSettingsConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = maps.Create(ctx, &corev1.ConfigMap{ObjectMeta: s.meta(ClaudeSettingsConfigMap), Data: map[string]string{"console.json": settings}}, metav1.CreateOptions{})
			return true, err
		}
		if err != nil || cur.Data["console.json"] == settings {
			return false, err
		}
		cur.Data = map[string]string{"console.json": settings}
		_, err = maps.Update(ctx, cur, metav1.UpdateOptions{})
		return true, err
	})
	if err != nil {
		return fmt.Errorf("writing the ConfigMap %s: %w", ClaudeSettingsConfigMap, err)
	}
	return nil
}

func (s *Service) meta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: s.cfg.Namespace, Labels: map[string]string{ManagedBy: managedByValue}}
}

// upsert runs a read-then-write until it doesn't lose a race (a conflict or a concurrent create),
// at most ten times.
func upsert(ctx context.Context, try func() (wrote bool, err error)) error {
	for attempt := 0; ; attempt++ {
		_, err := try()
		if (apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)) && attempt < 10 && ctx.Err() == nil {
			continue
		}
		return err
	}
}
