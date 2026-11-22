// internal/db/preflight_queries.go
package db

import (
	"context"
	"fmt"
)

// TemplateRow is a lightweight struct for template lookups.
type TemplateRow struct {
	TemplateID      string
	TemplateName    *string
	FingerprintJSON string
	RulesJSON       string
	FieldIDsJSON    *string
	SuccessCount    int
	FailCount       int
	LastUsedAt      *string
}

// GetTemplateByFingerprint looks up a template by org + fingerprint hash.
// Used by the preflight worker to find exact matches quickly.
func GetTemplateByFingerprint(ctx context.Context, orgID, bankName, docType, fingerprintHash string) (*TemplateRow, error) {
	row := pool.QueryRow(ctx,
		`SELECT template_id, template_name, fingerprint_json, rules_json, field_ids_json,
		        success_count, fail_count, last_used_at
		 FROM template_definitions
		 WHERE org_id = $1 AND bank_name = $2 AND document_type = $3 AND fingerprint_hash = $4 AND is_active = true
		 LIMIT 1`,
		orgID, bankName, docType, fingerprintHash,
	)

	var t TemplateRow
	err := row.Scan(&t.TemplateID, &t.TemplateName, &t.FingerprintJSON, &t.RulesJSON,
		&t.FieldIDsJSON, &t.SuccessCount, &t.FailCount, &t.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// IncrementTemplateSuccess bumps success_count and last_used_at.
func IncrementTemplateSuccess(ctx context.Context, templateID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE template_definitions
		 SET success_count = success_count + 1, last_used_at = now(), updated_at = now()
		 WHERE template_id = $1`,
		templateID,
	)
	return err
}

// IncrementTemplateFail bumps fail_count (drift signal).
func IncrementTemplateFail(ctx context.Context, templateID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE template_definitions
		 SET fail_count = fail_count + 1, updated_at = now()
		 WHERE template_id = $1`,
		templateID,
	)
	return err
}

// CreateOrgIfNotExists ensures an organization row exists. Named to match
// the original preflight package's function name, but points at the
// organizations table already used by CreateOrganization/GetOrganization
// in db.go — do not also create an "orgs" table, that would be a second,
// conflicting tenant table.
func CreateOrgIfNotExists(ctx context.Context, orgID, orgName string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO organizations (org_id, org_name, created_at) VALUES ($1, $2, now())
		 ON CONFLICT (org_id) DO NOTHING`,
		orgID, orgName,
	)
	return err
}

// GetUserOrgID returns the org_id for a user. Returns "" (not an error)
// if the user has no org assigned yet — callers should treat "" as
// "not yet in an org", not retry-worthy.
func GetUserOrgID(ctx context.Context, userID string) (string, error) {
	var orgID *string
	err := pool.QueryRow(ctx,
		`SELECT org_id FROM users WHERE id = $1`, userID,
	).Scan(&orgID)
	if err != nil {
		return "", err
	}
	if orgID == nil {
		return "", nil
	}
	return *orgID, nil
}

// EnsureUserOrg creates an org from the user's own ID if they don't have
// one yet (self-service, one-org-per-user default). Returns the org_id
// either way.
func EnsureUserOrg(ctx context.Context, userID, username string) (string, error) {
	existing, err := GetUserOrgID(ctx, userID)
	if err == nil && existing != "" {
		return existing, nil
	}

	orgID := userID // each user is their own org by default
	orgName := fmt.Sprintf("%s org", username)
	if err := CreateOrgIfNotExists(ctx, orgID, orgName); err != nil {
		return "", err
	}

	_, err = pool.Exec(ctx,
		`UPDATE users SET org_id = $2 WHERE id = $1`,
		userID, orgID,
	)
	return orgID, err
}
