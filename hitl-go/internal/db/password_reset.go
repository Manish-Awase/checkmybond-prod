// internal/db/password_reset.go
package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SavePasswordReset stores a SHA-256 hashed reset token with the user's email.
// The token expires after the given duration (typically 15 minutes).
func SavePasswordReset(ctx context.Context, tokenHash, email string, expiresAt time.Time) error {
	_, err := Pool().Exec(ctx, `
		INSERT INTO password_resets (token_hash, email, expires_at)
		VALUES ($1, $2, $3)
	`, tokenHash, email, expiresAt)
	return err
}

// ValidatePasswordReset looks up a token hash and returns the associated email
// if the token exists and has not expired. Returns an error if invalid or expired.
func ValidatePasswordReset(ctx context.Context, tokenHash string) (string, error) {
	var email string
	err := Pool().QueryRow(ctx, `
		SELECT email FROM password_resets
		WHERE token_hash = $1 AND expires_at > NOW()
	`, tokenHash).Scan(&email)
	if err == sql.ErrNoRows {
		return "", errors.New("invalid or expired reset token")
	}
	return email, err
}

// DeletePasswordReset removes a used/expired token from the database.
func DeletePasswordReset(ctx context.Context, tokenHash string) error {
	_, err := Pool().Exec(ctx, `DELETE FROM password_resets WHERE token_hash = $1`, tokenHash)
	return err
}

// UpdatePassword updates a user's password_hash by user ID.
func UpdatePassword(ctx context.Context, userID, newHash string) error {
	_, err := Pool().Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, newHash, userID)
	return err
}
