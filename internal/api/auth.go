package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/mail"
	"os"
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
	var input struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		RememberMe bool   `json:"rememberMe"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	var id, name, passwordHash, gender, country string
	var age *int16
	var emailVerified bool
	var banned bool
	var createdAt, updatedAt time.Time
	err := a.db.QueryRow(r.Context(),
		`SELECT id::text,name,password_hash,created_at,updated_at,age,gender,country,email_verified_at IS NOT NULL,banned_at IS NOT NULL
		 FROM users WHERE LOWER(email)=LOWER($1)`, email).
		Scan(&id, &name, &passwordHash, &createdAt, &updatedAt, &age, &gender, &country, &emailVerified, &banned)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect")
		return
	}
	if !emailVerified && a.emailVerificationEnabled() {
		writeError(w, http.StatusForbidden, "email_not_verified", "Verify your email address before signing in")
		return
	}
	if banned {
		writeError(w, http.StatusForbidden, "account_banned", "This account has been suspended")
		return
	}
	platform := detectUserPlatform(r.UserAgent())
	if _, err := a.db.Exec(r.Context(), `UPDATE users SET last_seen_at=NOW(),platform=$2 WHERE id=$1`, id, platform); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create session")
		return
	}
	a.recordUserActivity(r.Context(), id, http.MethodPost, "/api/auth/signin", http.StatusOK, platform)
	now := time.Now().UTC()
	token, err := signSessionToken(id, a.cfg.jwtSecret, now, input.RememberMe)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create session")
		return
	}
	lifetime := tokenLifetime
	if input.RememberMe {
		lifetime = rememberedTokenLifetime
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "expiresAt": now.Add(lifetime), "rememberMe": input.RememberMe,
		"user": map[string]any{
			"id": id, "name": name, "email": email, "age": age, "gender": gender, "country": country,
			"createdAt": createdAt, "updatedAt": updatedAt,
		},
	})
}

func (a *API) updateSession(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many session update attempts")
		return
	}
	var input struct {
		Password   string `json:"password"`
		RememberMe bool   `json:"rememberMe"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_session_update", "Password confirmation is required")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update session")
		return
	}
	defer tx.Rollback(r.Context())
	var name, email, passwordHash, gender, country string
	var age *int16
	var createdAt, updatedAt time.Time
	err = tx.QueryRow(r.Context(),
		`SELECT name,email,password_hash,created_at,updated_at,age,gender,country FROM users WHERE id=$1 FOR UPDATE`,
		userIDFrom(r)).Scan(&name, &email, &passwordHash, &createdAt, &updatedAt, &age, &gender, &country)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify account password")
		return
	}
	now := time.Now().UTC()
	token, err := signSessionToken(userIDFrom(r), a.cfg.jwtSecret, now, input.RememberMe)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create session")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update session")
		return
	}
	lifetime := tokenLifetime
	if input.RememberMe {
		lifetime = rememberedTokenLifetime
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "expiresAt": now.Add(lifetime), "rememberMe": input.RememberMe,
		"user": map[string]any{
			"id": userIDFrom(r), "name": name, "email": email, "age": age, "gender": gender, "country": country,
			"createdAt": createdAt, "updatedAt": updatedAt,
		},
	})
}

func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many profile update attempts")
		return
	}
	var input struct {
		Name     string          `json:"name"`
		Password string          `json:"password"`
		Age      json.RawMessage `json:"age"`
		Gender   *string         `json:"gender"`
		Country  *string         `json:"country"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 || input.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_profile", "A name up to 100 bytes and password confirmation are required")
		return
	}
	var age *int16
	if input.Age != nil && string(input.Age) != "null" {
		var parsed int
		if err := json.Unmarshal(input.Age, &parsed); err != nil || parsed < 1 || parsed > 120 {
			writeError(w, http.StatusBadRequest, "invalid_profile", "Age must be a whole number between 1 and 120")
			return
		}
		converted := int16(parsed)
		age = &converted
	}
	if input.Gender != nil {
		*input.Gender = strings.TrimSpace(*input.Gender)
		if *input.Gender != "" && *input.Gender != "woman" && *input.Gender != "man" &&
			*input.Gender != "non-binary" && *input.Gender != "self-described" &&
			*input.Gender != "prefer-not-to-say" {
			writeError(w, http.StatusBadRequest, "invalid_profile", "Choose one of the supported gender options")
			return
		}
	}
	if input.Country != nil {
		*input.Country = strings.TrimSpace(*input.Country)
		if len(*input.Country) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_profile", "Country must be 80 characters or fewer")
			return
		}
		for _, character := range *input.Country {
			if character < 32 || character == 127 {
				writeError(w, http.StatusBadRequest, "invalid_profile", "Country contains an invalid character")
				return
			}
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update profile")
		return
	}
	defer tx.Rollback(r.Context())
	var passwordHash string
	err = tx.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id=$1 FOR UPDATE`, userIDFrom(r)).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify account password")
		return
	}
	var name, email, gender, country string
	var savedAge *int16
	var createdAt, updatedAt time.Time
	if err := tx.QueryRow(r.Context(),
		`UPDATE users SET name=$1,
		 age=CASE WHEN $2 THEN $3::smallint ELSE age END,
		 gender=CASE WHEN $4 THEN $5 ELSE gender END,
		 country=CASE WHEN $6 THEN $7 ELSE country END,
		 updated_at=NOW()
		 WHERE id=$8 RETURNING name,email,age,gender,country,created_at,updated_at`,
		input.Name, input.Age != nil, age, input.Gender != nil, valueOrEmpty(input.Gender),
		input.Country != nil, valueOrEmpty(input.Country), userIDFrom(r)).
		Scan(&name, &email, &savedAge, &gender, &country, &createdAt, &updatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update profile")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": userIDFrom(r), "name": name, "email": email, "age": savedAge, "gender": gender,
		"country": country, "createdAt": createdAt, "updatedAt": updatedAt,
	}})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	var id, name, email, passwordHash, gender, country string
	var age *int16
	var emailVerified, emailNotifications bool
	var createdAt, updatedAt time.Time
	err := a.db.QueryRow(r.Context(),
		`SELECT id::text,name,email,password_hash,created_at,updated_at,age,gender,country,
		        email_verified_at IS NOT NULL,email_notifications
		 FROM users WHERE id=$1`, userIDFrom(r)).
		Scan(&id, &name, &email, &passwordHash, &createdAt, &updatedAt, &age, &gender, &country, &emailVerified, &emailNotifications)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Account no longer exists")
		return
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "name": name, "email": email, "age": age, "gender": gender, "country": country,
		"createdAt": createdAt, "updatedAt": updatedAt,
		"emailVerified": emailVerified, "emailNotifications": emailNotifications,
	})
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (a *API) verifyPassword(w http.ResponseWriter, r *http.Request) {
	if !a.allowRate(r, 10, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many password verification attempts")
		return
	}
	var input struct{ Password string }
	if !decodeJSON(w, r, &input) {
		return
	}
	var passwordHash string
	err := a.db.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id=$1`, userIDFrom(r)).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify account password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
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

func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var input struct{ Password string }
	if !decodeJSON(w, r, &input) {
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete account")
		return
	}
	defer tx.Rollback(r.Context())
	owner := userIDFrom(r)
	var passwordHash string
	err = tx.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify account password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Password is incorrect")
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT filename,kind FROM files WHERE owner_id=$1`, owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load account files")
		return
	}
	type ownedFile struct{ filename, kind string }
	var files []ownedFile
	for rows.Next() {
		var file ownedFile
		if err := rows.Scan(&file.filename, &file.kind); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load account files")
			return
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load account files")
		return
	}
	rows.Close()
	if _, err := tx.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, owner); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete account")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete account")
		return
	}
	removed := make(map[string]struct{})
	for _, file := range files {
		key := file.kind + "\x00" + file.filename
		if _, ok := removed[key]; ok {
			continue
		}
		removed[key] = struct{}{}
		var shared bool
		if err := a.db.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM files WHERE kind=$1 AND filename=$2)`,
			file.kind, file.filename).Scan(&shared); err != nil {
			log.Printf("could not check shared account upload %q: %v", file.filename, err)
			continue
		}
		if shared {
			continue
		}
		path, err := safeFilePath(a.cfg.uploadDir, file.kind, file.filename)
		if err == nil {
			err = os.Remove(path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("could not remove account upload %q: %v", file.filename, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
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
	input, ok := readErrorReportSubmission(w, r)
	if !ok {
		return
	}
	if err := validateErrorReportSubmission(userIDFrom(r), &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_error_report", err.Error())
		return
	}
	report, err := newErrorReport(userIDFrom(r), input.message, input.screen, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_error_report", err.Error())
		return
	}
	metadata, err := json.Marshal(input.metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_error_report", "Diagnostic metadata could not be encoded")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not save the error report")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		INSERT INTO error_reports(
			id,owner_id,title,message,details,reproduction_steps,screen,platform,metadata,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		report["id"], userIDFrom(r), input.title, report["message"], input.details, input.steps,
		report["screen"], detectUserPlatform(r.UserAgent()), metadata, report["createdAt"])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not save the error report")
		return
	}
	for _, attachment := range input.attachments {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO error_report_attachments(id,report_id,owner_id,filename,content_type,size_bytes,data)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			attachment.id, report["id"], userIDFrom(r), attachment.filename,
			attachment.contentType, len(attachment.data), attachment.data)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save report screenshots")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not save the error report")
		return
	}
	attachments := make([]map[string]any, 0, len(input.attachments))
	for _, attachment := range input.attachments {
		attachments = append(attachments, map[string]any{
			"id": attachment.id, "filename": attachment.filename,
			"contentType": attachment.contentType, "sizeBytes": len(attachment.data),
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sent": true, "attachments": attachments})
}

func validEmail(value string) bool {
	if len(value) > 254 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && strings.Contains(value, "@")
}
