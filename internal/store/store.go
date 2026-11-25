// Package store is containerservice's own data in Postgres: the owner's SSH keys, and Claude Code
// login and settings. Containers and volumes are Kubernetes objects and live only there.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Key is an SSH public key.
type Key struct {
	ID        string
	Line      string
	Label     string
	CreatedAt time.Time
}

var (
	// ErrNotFound: no such row.
	ErrNotFound = errors.New("not found")
	// ErrExists: the primary key is taken.
	ErrExists = errors.New("already exists")
)

//go:embed migrations/*.sql
var migrations embed.FS

// Postgres is the database.
type Postgres struct{ pool *pgxpool.Pool }

// Open connects and brings the schema up to date.
func Open(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	p := &Postgres{pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrating the database: %w", err)
	}
	return p, nil
}

// Close closes the pool.
func (p *Postgres) Close() { p.pool.Close() }

// migrate applies migrations/NNN_*.sql in order, each once, in its own transaction. A concurrent
// starter applying the same version waits on the row's lock and then finds it applied.
func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, version)
			if err != nil || tag.RowsAffected() == 0 {
				return err // applied already
			}
			_, err = tx.Exec(ctx, string(sql))
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
	}
	return nil
}

// AddKey inserts k if there are fewer than max keys and reports whether it did; ErrExists if it's
// listed already. max is a sanity bound for one owner, not a quota: concurrent adds may pass it by
// one each, which is fine.
func (p *Postgres) AddKey(ctx context.Context, k Key, max int) (bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO ssh_keys (id, line, label)
		SELECT $1, $2, $3 WHERE (SELECT count(*) FROM ssh_keys) < $4`, k.ID, k.Line, k.Label, max)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return false, ErrExists
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Keys is every key, oldest first.
func (p *Postgres) Keys(ctx context.Context) ([]Key, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, line, label, created_at FROM ssh_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) {
		var k Key
		err := r.Scan(&k.ID, &k.Line, &k.Label, &k.CreatedAt)
		return k, err
	})
}

// Key is the key with id.
func (p *Postgres) Key(ctx context.Context, id string) (Key, error) {
	var k Key
	err := p.pool.QueryRow(ctx, `SELECT id, line, label, created_at FROM ssh_keys WHERE id = $1`, id).Scan(&k.ID, &k.Line, &k.Label, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	return k, err
}

// DeleteKey removes the key with id; ErrNotFound if there is none.
func (p *Postgres) DeleteKey(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM ssh_keys WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyLines is every key's line, for authorized_keys.
func (p *Postgres) KeyLines(ctx context.Context) ([]string, error) {
	keys, err := p.Keys(ctx)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k.Line
	}
	return lines, nil
}

// Claude is the owner's Claude Code login and shared settings.
type Claude struct {
	// Token is a long-lived OAuth token ("" if none); TokenAt when it was set.
	Token   string
	TokenAt *time.Time
	// Settings is managed-settings JSON ("" if none).
	Settings   string
	SettingsAt *time.Time
}

// Claude reads the login and settings.
func (p *Postgres) Claude(ctx context.Context) (Claude, error) {
	var c Claude
	err := p.pool.QueryRow(ctx, `SELECT token, token_at, settings, settings_at FROM claude WHERE id = 1`).Scan(&c.Token, &c.TokenAt, &c.Settings, &c.SettingsAt)
	return c, err
}

// SetClaudeToken replaces the token ("" removes it).
func (p *Postgres) SetClaudeToken(ctx context.Context, token string) error {
	_, err := p.pool.Exec(ctx, `UPDATE claude SET token = $1, token_at = CASE WHEN $1 = '' THEN NULL ELSE now() END WHERE id = 1`, token)
	return err
}

// SetClaudeSettings replaces the settings JSON ("" removes it).
func (p *Postgres) SetClaudeSettings(ctx context.Context, settings string) error {
	_, err := p.pool.Exec(ctx, `UPDATE claude SET settings = $1, settings_at = CASE WHEN $1 = '' THEN NULL ELSE now() END WHERE id = 1`, settings)
	return err
}

// ClaudeConfig is the token and settings, for the containers.
func (p *Postgres) ClaudeConfig(ctx context.Context) (string, string, error) {
	c, err := p.Claude(ctx)
	return c.Token, c.Settings, err
}

// Identity is an identity the owner made: Name is theirs, ServiceAccount the account it is.
type Identity struct {
	Name           string
	ServiceAccount string
	CreatedAt      time.Time
}

// AddIdentity records an identity unless there are max already; ErrExists if the name is taken.
func (p *Postgres) AddIdentity(ctx context.Context, id Identity, max int) (bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO identities (name, service_account)
		SELECT $1, $2 WHERE (SELECT count(*) FROM identities) < $3`, id.Name, id.ServiceAccount, max)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return false, ErrExists
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Identities is every identity, by name.
func (p *Postgres) Identities(ctx context.Context) ([]Identity, error) {
	rows, err := p.pool.Query(ctx, `SELECT name, service_account, created_at FROM identities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Identity, error) {
		var id Identity
		err := r.Scan(&id.Name, &id.ServiceAccount, &id.CreatedAt)
		return id, err
	})
}

// Identity is the identity called name; ErrNotFound if there is none.
func (p *Postgres) Identity(ctx context.Context, name string) (Identity, error) {
	id := Identity{Name: name}
	err := p.pool.QueryRow(ctx, `SELECT service_account, created_at FROM identities WHERE name = $1`, name).Scan(&id.ServiceAccount, &id.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, ErrNotFound
	}
	return id, err
}

// DeleteIdentity removes the record and returns what it was (ErrNotFound if there was none). The
// record goes first, so that an attach that comes meanwhile finds no identity.
func (p *Postgres) DeleteIdentity(ctx context.Context, name string) (Identity, error) {
	id := Identity{Name: name}
	err := p.pool.QueryRow(ctx, `DELETE FROM identities WHERE name = $1 RETURNING service_account, created_at`, name).Scan(&id.ServiceAccount, &id.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, ErrNotFound
	}
	return id, err
}
