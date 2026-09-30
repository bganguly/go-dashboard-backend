package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var versionRe = regexp.MustCompile(`^V(\d+)__`)

type migration struct {
	version int
	path    string
}

// flywayVersions returns the number of versions Spring Boot Flyway has applied.
// Zero means the table doesn't exist or is empty (fresh DB).
func flywayVersions(ctx context.Context, pool *pgxpool.Pool) int {
	var tableExists bool
	_ = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'flyway_schema_history'
		)`).Scan(&tableExists)
	if !tableExists {
		return 0
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM flyway_schema_history`).Scan(&n)
	return n
}

func Run(pool *pgxpool.Pool, migrationsPath string) error {
	ctx := context.Background()

	if n := flywayVersions(ctx, pool); n > 0 {
		fmt.Printf("migrations: Spring Boot Flyway has applied %d versions — skipping Go migrations\n", n)
		return nil
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS go_schema_migrations (
			version     INTEGER PRIMARY KEY,
			filename    TEXT    NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create tracking table: %w", err)
	}

	entries, err := os.ReadDir(migrationsPath)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", migrationsPath, err)
	}

	var migrations []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m := versionRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		migrations = append(migrations, migration{version: v, path: filepath.Join(migrationsPath, name)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, mg := range migrations {
		var applied bool
		_ = pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM go_schema_migrations WHERE version = $1)`,
			mg.version,
		).Scan(&applied)
		if applied {
			continue
		}

		sql, err := os.ReadFile(mg.path)
		if err != nil {
			return fmt.Errorf("read %s: %w", mg.path, err)
		}

		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply V%d (%s): %w", mg.version, filepath.Base(mg.path), err)
		}

		filename := strings.TrimPrefix(filepath.Base(mg.path), "/")
		if _, err := pool.Exec(ctx,
			`INSERT INTO go_schema_migrations (version, filename) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			mg.version, filename,
		); err != nil {
			return fmt.Errorf("record V%d: %w", mg.version, err)
		}
	}
	return nil
}
