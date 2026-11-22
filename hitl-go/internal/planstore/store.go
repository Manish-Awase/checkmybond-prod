// internal/planstore/store.go
//
// Factory only — the interface itself lives in internal/interfaces/planstore.go.
package planstore

import (
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"hitl-platform/internal/interfaces"
)

// NewStore builds an interfaces.PlanTransactionStore backed by whichever
// driver is configured.
//
//	driver == "postgres" (default): pass the existing *pgxpool.Pool (e.g. internal/db.Pool()).
//	driver == "mysql":              pass a *sql.DB opened with sql.Open("mysql", dsn).
func NewStore(driver string, pgPool *pgxpool.Pool, mysqlDB *sql.DB) (interfaces.PlanTransactionStore, error) {
	switch driver {
	case "", "postgres":
		if pgPool == nil {
			return nil, fmt.Errorf("planstore: postgres pool is nil")
		}
		return NewPostgresStore(pgPool), nil
	case "mysql":
		if mysqlDB == nil {
			return nil, fmt.Errorf("planstore: mysql db is nil")
		}
		return NewMySQLStore(mysqlDB), nil
	default:
		return nil, fmt.Errorf("planstore: unknown driver %q (want \"postgres\" or \"mysql\")", driver)
	}
}
