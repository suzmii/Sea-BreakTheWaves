package infrastructure

import (
	"context"
	"database/sql"
	"time"

	"recommendation_v2/config"

	_ "github.com/jackc/pgx/v5/stdlib"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

var pgDB *sql.DB

func Postgres() *sql.DB {
	return pgDB
}

func InitPostgres() error {
	db, err := sql.Open("pgx", config.Cfg.Postgres.DSN)
	if err != nil {
		return err
	}

	if config.Cfg.Postgres.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.Cfg.Postgres.MaxOpenConns)
	}
	if config.Cfg.Postgres.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.Cfg.Postgres.MaxIdleConns)
	}
	if config.Cfg.Postgres.ConnMaxLifetimeSeconds > 0 {
		db.SetConnMaxLifetime(time.Duration(config.Cfg.Postgres.ConnMaxLifetimeSeconds) * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return err
	}

	// Schema managed by migrate CLI: migrate -path migrations -database "$DSN" up

	pgDB = db
	log.Info("[infra] postgres initialized")
	return nil
}

