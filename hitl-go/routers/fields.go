// routers/fields.go
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

func FieldsRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)

	r.Get("/", handleListFields)
	r.Post("/", handleCreateField)
	r.Post("/seed", handleSeedFields)
	r.Put("/{id}", handleUpdateField)
	r.Delete("/{id}", handleDeleteField)

	return r
}

type fieldRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	FieldType   string    `json:"field_type"`
	Aliases     []string  `json:"aliases"`
	Category    string    `json:"category"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

func handleListFields(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := idb.Pool().Query(ctx,
		`SELECT id, name, description, field_type, aliases, category, is_active, created_at
		 FROM field_library WHERE is_active = true ORDER BY category, name`)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	fields := []fieldRecord{}
	for rows.Next() {
		var f fieldRecord
		var aliases []string
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.FieldType, &aliases, &f.Category, &f.IsActive, &f.CreatedAt); err != nil {
			continue
		}
		f.Aliases = aliases
		fields = append(fields, f)
	}

	writeJSON(w, http.StatusOK, fields)
}

func handleCreateField(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		FieldType   string   `json:"field_type"`
		Aliases     []string `json:"aliases"`
		Category    string   `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.FieldType == "" {
		errJSON(w, http.StatusBadRequest, "name and field_type required")
		return
	}
	if req.Category == "" {
		req.Category = "custom"
	}
	aliasesJSON, _ := json.Marshal(req.Aliases)

	id := uuid.New().String()
	_, err := idb.Pool().Exec(ctx,
		`INSERT INTO field_library (id, name, description, field_type, aliases, category, is_active, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, true, now(), now())`,
		id, req.Name, req.Description, req.FieldType, aliasesJSON, req.Category,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create field")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "created"})
}

func handleUpdateField(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		FieldType   string   `json:"field_type"`
		Aliases     []string `json:"aliases"`
		Category    string   `json:"category"`
		IsActive    *bool    `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}

	// nil (not just empty) when the client omits aliases entirely, so
	// COALESCE($5, aliases) below preserves the existing value -- a
	// marshaled "null" would NOT be SQL NULL and would overwrite it.
	var aliasesJSON []byte
	if req.Aliases != nil {
		aliasesJSON, _ = json.Marshal(req.Aliases)
	}

	_, err := idb.Pool().Exec(ctx,
		`UPDATE field_library
		 SET name = COALESCE(NULLIF($2,''), name),
		     description = COALESCE(NULLIF($3,''), description),
		     field_type = COALESCE(NULLIF($4,''), field_type),
		     aliases = COALESCE($5, aliases),
		     category = COALESCE(NULLIF($6,''), category),
		     is_active = COALESCE($7, is_active),
		     updated_at = now()
		 WHERE id = $1`,
		id, req.Name, req.Description, req.FieldType, aliasesJSON, req.Category, req.IsActive,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not update field")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "updated"})
}

func handleDeleteField(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	_, err := idb.Pool().Exec(ctx,
		`UPDATE field_library SET is_active = false, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not delete field")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "deleted"})
}

func handleSeedFields(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var count int
	err := idb.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM field_library WHERE category IN ('critical','standard')`).Scan(&count)
	if err == nil && count > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"seeded": false, "message": "already seeded"})
		return
	}

	standardFields := []struct {
		Name        string
		Description string
		FieldType   string
		Aliases     []string
		Category    string
	}{
		{"Issuer Name", "Legal name of the bond issuer", "text", []string{"Issuer", "Obligor", "Borrower"}, "critical"},
		{"Issue Date", "Date when the bond is issued", "date", []string{"Dated Date", "Closing Date"}, "critical"},
		{"Maturity Date", "Final maturity date of the bond", "date", []string{"Final Maturity", "Redemption Date"}, "critical"},
		{"Coupon Rate", "Annual interest rate", "percentage", []string{"Interest Rate", "Coupon"}, "critical"},
		{"Face Value", "Principal amount per bond", "currency", []string{"Principal Amount", "Par Value", "Denomination"}, "critical"},
		{"ISIN", "International Securities Identification Number", "text", []string{"ISIN Code", "Security ID"}, "critical"},
		{"CUSIP", "CUSIP identifier", "text", []string{"CUSIP Number"}, "standard"},
		{"Credit Rating Moodys", "Moody's credit rating", "text", []string{"Moody's Rating"}, "standard"},
		{"Credit Rating SP", "S&P credit rating", "text", []string{"S&P Rating", "Standard & Poor's"}, "standard"},
		{"Credit Rating Fitch", "Fitch credit rating", "text", []string{"Fitch Rating"}, "standard"},
		{"Lead Manager", "Lead underwriter or bookrunner", "text", []string{"Lead Underwriter", "Bookrunner", "Arranger"}, "standard"},
		{"Interest Payment Frequency", "Frequency of coupon payments", "text", []string{"Payment Frequency", "Coupon Frequency"}, "standard"},
		{"Day Count Convention", "Interest calculation convention", "text", []string{"Day Count", "Interest Basis"}, "standard"},
		{"Redemption Type", "Type of redemption provision", "text", []string{"Redemption Provision"}, "standard"},
		{"Call Provision", "Callable bond terms", "text", []string{"Optional Redemption", "Call Schedule"}, "standard"},
		{"Put Provision", "Puttable bond terms", "text", []string{"Optional Put", "Investor Put"}, "standard"},
		{"Guarantee", "Guarantor information if guaranteed", "text", []string{"Guarantor", "Credit Support"}, "standard"},
		{"Governing Law", "Legal jurisdiction", "text", []string{"Governing Law", "Jurisdiction"}, "standard"},
		{"Use of Proceeds", "Purpose of bond proceeds", "text", []string{"Proceeds Use", "Application of Proceeds"}, "standard"},
		{"Total Issue Size", "Total principal amount of issuance", "currency", []string{"Aggregate Principal", "Total Offering"}, "critical"},
		{"Listing Exchange", "Exchange where bond is listed", "text", []string{"Stock Exchange", "Listed On"}, "standard"},
		{"Trustee", "Bond trustee name", "text", []string{"Fiscal Agent", "Trustee"}, "standard"},
		{"Paying Agent", "Paying agent name", "text", []string{"Principal Paying Agent"}, "standard"},
		{"Registrar", "Bond registrar name", "text", []string{"Transfer Agent", "Registrar"}, "standard"},
		{"Form of Notes", "Registered or bearer form", "text", []string{"Form of Security"}, "standard"},
		{"Tax Treatment", "Tax considerations summary", "text", []string{"Taxation", "Tax Provisions"}, "standard"},
		{"Events of Default", "Events triggering default", "text", []string{"Default Events"}, "standard"},
		{"Cross Default Threshold", "Threshold for cross-default", "currency", []string{"Cross Default Amount"}, "standard"},
		{"Change of Control Put", "Change of control redemption terms", "text", []string{"Change of Control"}, "standard"},
		{"Make Whole Premium", "Make-whole call premium terms", "text", []string{"Make Whole"}, "standard"},
		{"Green Bond", "Green / sustainability-linked flag", "boolean", []string{"Sustainability Linked", "ESG Bond"}, "standard"},
	}

	seeded := 0
	for _, f := range standardFields {
		id := uuid.New().String()
		_, _ = idb.Pool().Exec(ctx,
			`INSERT INTO field_library (id, name, description, field_type, aliases, category, is_active, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, true, now(), now())
			 ON CONFLICT DO NOTHING`,
			id, f.Name, f.Description, f.FieldType, f.Aliases, f.Category,
		)
		seeded++
	}

	writeJSON(w, http.StatusOK, map[string]any{"seeded": true, "count": seeded})
}
