package store

import (
	"context"
	"errors"
	"os"
	"testing"
)

// The tests need a Postgres database they may own: CONTAINERSERVICE_TEST_DATABASE_URL.
func open(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("CONTAINERSERVICE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CONTAINERSERVICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	p, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.pool.Exec(ctx, `TRUNCATE ssh_keys`); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMigrateTwice(t *testing.T) {
	p := open(t)
	if err := p.migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestKeys(t *testing.T) {
	p := open(t)
	ctx := context.Background()
	for i, id := range []string{"a", "b"} {
		added, err := p.AddKey(ctx, Key{ID: id, Line: "ssh-ed25519 " + id, Label: "k"}, 2)
		if err != nil || !added {
			t.Fatalf("key %d: %v %v", i, added, err)
		}
	}
	if _, err := p.AddKey(ctx, Key{ID: "a", Line: "x"}, 10); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	if added, err := p.AddKey(ctx, Key{ID: "c", Line: "x"}, 2); err != nil || added {
		t.Errorf("over max: %v %v", added, err)
	}
	lines, err := p.KeyLines(ctx)
	if err != nil || len(lines) != 2 || lines[0] != "ssh-ed25519 a" {
		t.Errorf("lines: %v %v", lines, err)
	}
	if k, err := p.Key(ctx, "b"); err != nil || k.Label != "k" {
		t.Errorf("key b: %+v %v", k, err)
	}
	if err := p.DeleteKey(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteKey(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := p.Key(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted key: %v", err)
	}
}

func TestClaude(t *testing.T) {
	p := open(t)
	ctx := context.Background()
	if err := p.SetClaudeToken(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetClaudeSettings(ctx, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	c, err := p.Claude(ctx)
	if err != nil || c.Token != "tok" || c.TokenAt == nil || c.Settings != `{"a":1}` || c.SettingsAt == nil {
		t.Fatalf("%+v %v", c, err)
	}
	p.SetClaudeToken(ctx, "")
	if c, _ := p.Claude(ctx); c.Token != "" || c.TokenAt != nil {
		t.Errorf("after clearing: %+v", c)
	}
}
