package api

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

func (a *API) signup(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many signup attempts")
		return
	}
	var input struct{ Email, Password, Name string }
	if !decodeJSON(w, r, &input) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if !validEmail(email) || len(input.Password) < 8 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_signup", "Provide a valid email and a password of 8 to 72 bytes")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create account")
		return
	}
	id, now := newUUID(), time.Now().UTC()
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO users(id,name,email,password_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`,
		id, strings.TrimSpace(input.Name), email, string(hash), now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "email_in_use", "An account with that email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create account")
		return
	}
	if !a.emailVerificationEnabled() {
		writeJSON(w, http.StatusCreated, map[string]any{
			"verificationRequired": false,
			"emailVerified":        false,
			"email":                email,
		})
		return
	}
	verificationToken, tokenHash, err := newVerificationToken()
	if err != nil {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, id)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create email verification token")
		return
	}
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO email_verification_tokens(user_id,token_hash,expires_at,created_at)
		 VALUES($1,$2,$3,$4)`,
		id, tokenHash, now.Add(24*time.Hour), now)
	if err != nil {
		_, _ = a.db.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, id)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create email verification token")
		return
	}
	if err := a.sendVerificationEmail(r.Context(), email, strings.TrimSpace(input.Name), verificationToken); err != nil {
		log.Printf("verification email delivery failed: %v", err)
		writeError(w, http.StatusServiceUnavailable, "email_delivery_failed", "Your account was created, but the verification email could not be sent. Request a new verification email to try again.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"verificationRequired": true,
		"emailVerified":        false,
		"email":                email,
	})
}

func (a *API) signin(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 20, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many signin attempts")
		return
	}
	var input struct{ Email, Password string }
	if !decodeJSON(w, r, &input) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	var id, name, passwordHash string
	var emailVerified bool
	var createdAt, updatedAt time.Time
	err := a.db.QueryRow(r.Context(),
		`SELECT id::text,name,password_hash,created_at,updated_at,email_verified_at IS NOT NULL
		 FROM users WHERE LOWER(email)=LOWER($1)`, email).
		Scan(&id, &name, &passwordHash, &createdAt, &updatedAt, &emailVerified)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
		return
	}
	if !emailVerified && a.emailVerificationEnabled() {
		writeError(w, http.StatusForbidden, "email_not_verified", "Verify your email address before signing in")
		return
	}
	token, err := signToken(id, a.cfg.jwtSecret, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": map[string]any{"id": id, "name": name, "email": email, "createdAt": createdAt, "updatedAt": updatedAt}})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	var id, name, email, passwordHash string
	var emailVerified, emailNotifications bool
	var createdAt, updatedAt time.Time
	err := a.db.QueryRow(r.Context(),
		`SELECT id::text,name,email,password_hash,created_at,updated_at,
		        email_verified_at IS NOT NULL,email_notifications
		 FROM users WHERE id=$1`, userIDFrom(r)).
		Scan(&id, &name, &email, &passwordHash, &createdAt, &updatedAt, &emailVerified, &emailNotifications)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Account no longer exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "name": name, "email": email, "createdAt": createdAt, "updatedAt": updatedAt,
		"emailVerified": emailVerified, "emailNotifications": emailNotifications,
	})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct{ CurrentPassword, NewPassword string }
	if !decodeJSON(w, r, &input) || len(input.NewPassword) < 8 || len(input.NewPassword) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_password", "New password must be 8 to 72 bytes")
		return
	}
	var hash string
	err := a.db.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, userIDFrom(r)).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Current password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update password")
		return
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE users SET password_hash=$1,updated_at=NOW() WHERE id=$2`, string(newHash), userIDFrom(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update password")
		return
	}
	now := time.Now().UTC()
	if err := a.sendOptInNotification(
		r.Context(),
		userIDFrom(r),
		"account-security",
		now.Format(time.RFC3339Nano),
		"Shelve account security alert",
		"Your Shelve account password was changed. If you did not make this change, secure your account immediately.",
	); err != nil {
		log.Printf("password-change notification email failed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

func newErrorReport(userID, message, screen string, createdAt time.Time) (map[string]any, error) {
	message = strings.TrimSpace(message)
	if userID == "" {
		return nil, errors.New("An authenticated account is required")
	}
	if message == "" || len(message) > maxErrorReportMessage {
		return nil, errors.New("Error message must contain between 1 and 4000 bytes")
	}
	screen = strings.TrimSpace(screen)
	if len(screen) > maxErrorReportScreen {
		return nil, errors.New("Screen identifier is too long")
	}
	for _, character := range screen {
		if character != '-' && (character < 'a' || character > 'z') {
			return nil, errors.New("Screen identifier is invalid")
		}
	}
	return map[string]any{"id": newUUID(), "userId": userID, "message": message, "screen": screen, "createdAt": createdAt.UTC()}, nil
}

func (a *API) createErrorReport(w http.ResponseWriter, r *http.Request) {
	var input struct{ Message, Screen string }
	if !decodeJSON(w, r, &input) {
		return
	}
	report, err := newErrorReport(userIDFrom(r), input.Message, input.Screen, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_error_report", err.Error())
		return
	}
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO error_reports(id,owner_id,message,screen,created_at) VALUES($1,$2,$3,$4,$5)`,
		report["id"], userIDFrom(r), report["message"], report["screen"], report["createdAt"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not save the error report")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"sent": true})
}

func validEmail(value string) bool {
	if len(value) > 254 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && strings.Contains(value, "@")
}
