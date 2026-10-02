package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Open(ctx context.Context, url string, runtime bool) (*pgxpool.Pool, error) {
	role := ""
	if runtime {
		role = "desk_app"
	}
	return OpenRole(ctx, url, role)
}
func OpenRole(ctx context.Context, url, role string) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, e
	}
	c.MaxConns = 8
	c.MinConns = 0
	c.MaxConnLifetime = 30 * time.Minute
	if role != "" {
		c.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, e := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize()+"; SET statement_timeout='8s'; SET idle_in_transaction_session_timeout='10s'")
			return e
		}
	}
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return p, nil
}

// Migrate applies ordered, checksum-protected forward migrations under one database lock.
// The path identifies the first migration; siblings in that directory are discovered.
func Migrate(ctx context.Context, p *pgxpool.Pool, path string) error {
	entries, e := os.ReadDir(filepath.Dir(path))
	if e != nil {
		return e
	}
	type migration struct {
		version int
		name    string
	}
	files := []migration{}
	seen := map[int]bool{}
	pattern := regexp.MustCompile(`^([0-9]+)_.+\.sql$`)
	for _, entry := range entries {
		m := pattern.FindStringSubmatch(entry.Name())
		if entry.IsDir() || m == nil {
			continue
		}
		version, e := strconv.Atoi(m[1])
		if e != nil || version < 1 || seen[version] {
			return fmt.Errorf("invalid or duplicate migration version: %s", entry.Name())
		}
		seen[version] = true
		files = append(files, migration{version, entry.Name()})
	}
	if len(files) == 0 {
		return errors.New("no migrations found")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(728183)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version int PRIMARY KEY);ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); e != nil {
		return e
	}
	for _, m := range files {
		b, e := os.ReadFile(filepath.Join(filepath.Dir(path), m.name))
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		checksum := hex.EncodeToString(sum[:])
		var stored *string
		e = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, m.version).Scan(&stored)
		if e == nil {
			if stored != nil && *stored != checksum {
				return fmt.Errorf("migration %s changed after application", m.name)
			}
			if stored == nil {
				if _, e = tx.Exec(ctx, `UPDATE schema_migrations SET checksum=$2 WHERE version=$1`, m.version, checksum); e != nil {
					return e
				}
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, string(b)); e != nil {
			return fmt.Errorf("migration %s: %w", m.name, e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, m.version, checksum); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func Seed(ctx context.Context, p *pgxpool.Pool) error {
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_, e = tx.Exec(ctx, `INSERT INTO users(id,name,role) VALUES('maya','Maya Chen','analyst'),('alex','Alex Rivera','approver'),('sam','Sam Okafor','controller'),('jules','Jules Patel','controller'),('viewer','Guest observer','viewer') ON CONFLICT DO NOTHING;
 INSERT INTO periods(month) VALUES('2026-08-01'),('2026-09-01'),('2026-10-01') ON CONFLICT DO NOTHING;
 INSERT INTO customers VALUES('northstar','Northstar Media','IO-1042',2500000),('cedar','Cedar & Coast','IO-1087',1000000),('atlas','Atlas Audio','IO-1103',5000000),('kinetic','Kinetic Studio','IO-1121',2000000) ON CONFLICT DO NOTHING;
 INSERT INTO contracts VALUES('northstar-v1','northstar',1,12.3456),('cedar-v1','cedar',1,8.500000),('atlas-v1','atlas',1,16.250000),('kinetic-v1','kinetic',1,10.750000) ON CONFLICT DO NOTHING;
 INSERT INTO usage_facts(customer_id,month,impressions) SELECT c.id,p.month,CASE c.id WHEN 'northstar' THEN 1450000 WHEN 'cedar' THEN 825000 WHEN 'atlas' THEN 2010000 ELSE 1120000 END FROM customers c CROSS JOIN periods p ON CONFLICT DO NOTHING;`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func JSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

var ErrConflict = errors.New("conflict")

// Provision grants non-owner login identities their scoped runtime roles after migration.
func Provision(ctx context.Context, p *pgxpool.Pool, app, erp string) error {
	for role, user := range map[string]string{"desk_app": app, "desk_erp": erp} {
		if user == "" {
			continue
		}
		if _, e := p.Exec(ctx, "GRANT "+pgx.Identifier{role}.Sanitize()+" TO "+pgx.Identifier{user}.Sanitize()); e != nil {
			return e
		}
	}
	return nil
}

// AssertRuntimeIdentity rejects owner or role-administration credentials in hosted processes.
func AssertRuntimeIdentity(ctx context.Context, p *pgxpool.Pool) error {
	var elevated bool
	e := p.QueryRow(ctx, `SELECT r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname IN ('public','erp') AND pg_has_role(session_user,n.nspowner,'MEMBER')) FROM pg_roles r WHERE r.rolname=session_user`).Scan(&elevated)
	if e != nil {
		return e
	}
	if elevated {
		return errors.New("hosted runtime requires non-owner credentials without role administration privileges")
	}
	return nil
}
