package migrate

import (
	"errors"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func Run(databaseURL, migrationsPath string) error {
	dbURL := databaseURL
	if strings.HasPrefix(dbURL, "postgresql://") {
		dbURL = "pgx5://" + dbURL[len("postgresql://"):]
	} else if strings.HasPrefix(dbURL, "postgres://") {
		dbURL = "pgx5://" + dbURL[len("postgres://"):]
	}
	m, err := migrate.New("file://"+migrationsPath, dbURL)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
