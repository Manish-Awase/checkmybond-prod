// routers/schemas.go
//
// GET /api/schemas is already called by the frontend (lib/api.ts's
// getSchemas()), but no implementation for it exists anywhere in this
// codebase that I have visibility into -- possibly it lives in a file
// I don't have, in which case mounting this at /api/schemas will
// collide. Check before deploying; if a schemas route already exists
// elsewhere, keep only the POST handler here and mount it into that
// existing router instead of calling SchemasRouter() from main.go.
//
// POST /api/schemas is new: it's what lets the desktop app's ad-hoc
// Excel-parsed schema (built client-side in LlmSchemaUpload.tsx) become
// a real schema_id, which routers/desktop.go's handleCreateDesktopJob
// requires. The website's /api/documents/upload apparently does this
// registration inline as part of the upload handler; this gives the
// desktop flow (which registers a schema BEFORE creating the job,
// since it doesn't upload a PDF to the server at all) the same
// capability as its own explicit step.
package routers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func SchemasRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Get("/", handleListSchemas)
	r.Post("/", handleRegisterSchema)
	return r
}

func handleListSchemas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := idb.Pool().Query(ctx,
		`SELECT schema_id, schema_name, version, created_at
		 FROM schema_registry ORDER BY created_at DESC`)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var schemaID, schemaName, version string
		var createdAt time.Time
		if err := rows.Scan(&schemaID, &schemaName, &version, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"schema_id":   schemaID,
			"schema_name": schemaName,
			"version":     version,
			"created_at":  createdAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRegisterSchema accepts the same JSON shape
// backend/services/schema_models.py's SchemaDefinition expects
// ({schema_name, version, fields: [...]});  LlmSchemaUpload.tsx already
// builds exactly this shape client-side from the parsed Excel file, so
// no translation is needed on either side.
func handleRegisterSchema(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		SchemaName string          `json:"schema_name"`
		Version    string          `json:"version"`
		Fields     json.RawMessage `json:"fields"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SchemaName == "" || len(req.Fields) == 0 {
		errJSON(w, http.StatusBadRequest, "schema_name and fields required")
		return
	}
	if req.Version == "" {
		req.Version = "1.0"
	}

	definition, err := json.Marshal(map[string]any{
		"schema_name": req.SchemaName,
		"version":     req.Version,
		"fields":      json.RawMessage(req.Fields),
	})
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not encode schema")
		return
	}

	schemaID := uuid.New().String()
	_, err = idb.Pool().Exec(ctx,
		`INSERT INTO schema_registry (schema_id, schema_name, version, definition_json, created_at, created_by)
		 VALUES ($1, $2, $3, $4, now(), $5)`,
		schemaID, req.SchemaName, req.Version, string(definition), imw.UserID(r),
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not register schema")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"schema_id": schemaID})
}
