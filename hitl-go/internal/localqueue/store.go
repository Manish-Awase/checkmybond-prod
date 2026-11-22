// hitl-go/internal/localqueue/store.go
package localqueue

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore implements Store against the real extraction_jobs table
// (see backend/models/db.py's ExtractionJob — same table hitl-server
// already reads/writes via internal/db).
type PgStore struct {
	Pool *pgxpool.Pool
}

func (s *PgStore) MarkFailed(ctx context.Context, extractionID string, errStr string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE extraction_jobs SET status='failed', error=$1 WHERE extraction_id=$2`,
		errStr, extractionID,
	)
	return err
}
