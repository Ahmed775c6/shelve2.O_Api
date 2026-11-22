package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *API) emailVerificationEnabled() bool {
	return a.cfg.mailer != nil && strings.TrimSpace(a.cfg.verificationBaseURL) != ""
}

func newVerificationToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, fmt.Sprintf("%x", hash), nil
}

func (a *API) sendVerificationEmail(ctx context.Context, address, name, token string) error {
	if a.cfg.mailer == nil {
		return errors.New("email delivery is not configured")
	}
	verificationURL, err := url.Parse(a.cfg.verificationBaseURL)
	if err != nil {
		return errors.New("email verification URL is invalid")
	}
	query := verificationURL.Query()
	query.Set("token", token)
	verificationURL.RawQuery = query.Encode()
	greeting := "Hello,"
	if name != "" {
		greeting = "Hello " + name + ","
	}
	body := fmt.Sprintf(
		"%s\n\nVerify your Shelve email address using this link:\n%s\n\nThis link expires in 24 hours. If you did not create this account, you can ignore this email.",
		greeting, verificationURL.String(),
	)
	return a.cfg.mailer.Send(ctx, address, "Verify your Shelve email address", body)
}

func (a *API) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 30, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many email verification attempts")
		return
	}
	var input struct{ Token string }
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Token) < 32 || len(input.Token) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_verification", "A valid verification token is required")
		return
	}
	hash := sha256.Sum256([]byte(input.Token))
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify email")
		return
	}
	defer tx.Rollback(r.Context())
	var owner string
	err = tx.QueryRow(r.Context(),
		`SELECT user_id::text FROM email_verification_tokens
		 WHERE token_hash=$1 AND expires_at>NOW() FOR UPDATE`,
		fmt.Sprintf("%x", hash)).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "invalid_verification", "This verification link is invalid or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify email")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE users SET email_verified_at=NOW() WHERE id=$1`, owner); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify email")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM email_verification_tokens WHERE user_id=$1`, owner); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not finish email verification")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not finish email verification")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
}

func (a *API) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 5, time.Hour) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many verification email requests")
		return
	}
	if a.cfg.mailer == nil || a.cfg.verificationBaseURL == "" {
		writeError(w, http.StatusServiceUnavailable, "email_unavailable", "Email verification is not configured on this server")
		return
	}
	var input struct{ Email string }
	if !decodeJSON(w, r, &input) {
		return
	}
	address := strings.ToLower(strings.TrimSpace(input.Email))
	if !validEmail(address) {
		writeError(w, http.StatusBadRequest, "invalid_email", "Provide a valid email address")
		return
	}
	var owner, name string
	var verified bool
	err := a.db.QueryRow(r.Context(),
		`SELECT id::text,name,email_verified_at IS NOT NULL FROM users WHERE LOWER(email)=LOWER($1)`,
		address).Scan(&owner, &name, &verified)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && verified) {
		writeJSON(w, http.StatusAccepted, map[string]any{"message": "If the account needs verification, an email has been sent."})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not request verification email")
		return
	}
	token, tokenHash, err := newVerificationToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create email verification token")
		return
	}
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO email_verification_tokens(user_id,token_hash,expires_at,created_at)
		 VALUES($1,$2,NOW()+INTERVAL '24 hours',NOW())
		 ON CONFLICT(user_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,
		   expires_at=EXCLUDED.expires_at,created_at=EXCLUDED.created_at`,
		owner, tokenHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create email verification token")
		return
	}
	if err := a.sendVerificationEmail(r.Context(), address, name, token); err != nil {
		log.Printf("verification email delivery failed: %v", err)
		writeError(w, http.StatusServiceUnavailable, "email_delivery_failed", "The verification email could not be sent. Please try again later.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message": "If the account needs verification, an email has been sent."})
}

func (a *API) emailNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var enabled, verified bool
		err := a.db.QueryRow(r.Context(),
			`SELECT email_notifications,email_verified_at IS NOT NULL FROM users WHERE id=$1`,
			userIDFrom(r)).Scan(&enabled, &verified)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load email notification preferences")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"emailNotifications":     enabled,
			"emailVerified":          verified,
			"emailDeliveryAvailable": a.cfg.mailer != nil,
		})
	case http.MethodPatch:
		var input struct {
			Enabled *bool `json:"emailNotifications"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Enabled == nil {
			writeError(w, http.StatusBadRequest, "invalid_preferences", "emailNotifications must be true or false")
			return
		}
		if *input.Enabled && a.cfg.mailer == nil {
			writeError(w, http.StatusServiceUnavailable, "email_unavailable", "Email delivery is not configured on this server")
			return
		}
		tag, err := a.db.Exec(r.Context(),
			`UPDATE users SET email_notifications=$1
			 WHERE id=$2 AND ($1=FALSE OR email_verified_at IS NOT NULL)`,
			*input.Enabled, userIDFrom(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save email notification preferences")
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, http.StatusConflict, "email_not_verified", "Verify your email address before enabling email notifications")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"emailNotifications": *input.Enabled})
	default:
		methodNotAllowed(w, "GET, PATCH")
	}
}

func (a *API) sendOptInNotification(ctx context.Context, owner, eventType, eventKey, subject, body string) error {
	if a.cfg.mailer == nil {
		return nil
	}
	var address string
	var enabled bool
	err := a.db.QueryRow(ctx,
		`SELECT email,email_notifications FROM users
		 WHERE id=$1 AND email_verified_at IS NOT NULL`,
		owner).Scan(&address, &enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load email notification settings: %w", err)
	}
	tag, err := a.db.Exec(ctx,
		`INSERT INTO email_notification_sends(owner_id,event_type,event_key)
		 VALUES($1,$2,$3) ON CONFLICT DO NOTHING`,
		owner, eventType, eventKey)
	if err != nil {
		return fmt.Errorf("reserve email notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := a.cfg.mailer.Send(ctx, address, subject, body); err != nil {
		_, _ = a.db.Exec(ctx,
			`DELETE FROM email_notification_sends
			 WHERE owner_id=$1 AND event_type=$2 AND event_key=$3`,
			owner, eventType, eventKey)
		return fmt.Errorf("send email notification: %w", err)
	}
	return nil
}
