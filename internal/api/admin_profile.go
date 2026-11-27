package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const (
	adminProfileEmail  = "ahmed.chouikh2020@gmail.com"
	maxAdminAvatarSize = 512 << 10
)

var adminUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{2,64}$`)

func (a *API) authenticatePrimaryAdmin(ctx context.Context, username, password string) (bool, error) {
	if a.db == nil {
		return secureStringEqual(username, a.cfg.adminUsername) &&
			a.cfg.adminPassword != "" && secureStringEqual(password, a.cfg.adminPassword), nil
	}

	var storedUsername, passwordHash string
	err := a.db.QueryRow(ctx, `SELECT username,password_hash FROM admin_profiles WHERE profile_key='primary'`).
		Scan(&storedUsername, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		if !secureStringEqual(username, a.cfg.adminUsername) || a.cfg.adminPassword == "" ||
			!secureStringEqual(password, a.cfg.adminPassword) {
			return false, nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return false, err
		}
		_, err = a.db.Exec(ctx, `
			INSERT INTO admin_profiles(profile_key,username,email,password_hash)
			VALUES('primary',$1,$2,$3) ON CONFLICT(profile_key) DO NOTHING`,
			username, adminProfileEmail, string(hash))
		if err != nil {
			return false, err
		}
		err = a.db.QueryRow(ctx, `SELECT username,password_hash FROM admin_profiles WHERE profile_key='primary'`).
			Scan(&storedUsername, &passwordHash)
	}
	if err != nil {
		return false, err
	}
	return secureStringEqual(username, storedUsername) &&
		bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) == nil, nil
}

func (a *API) verifyAdminPassword(r *http.Request, password string) (bool, error) {
	name, role := adminNameFrom(r), adminRoleFrom(r)
	if role == "developer" {
		return a.cfg.developerPassword != "" &&
			secureStringEqual(name, a.cfg.developerUsername) &&
			secureStringEqual(password, a.cfg.developerPassword), nil
	}
	if role != "admin" {
		return false, nil
	}
	if a.db != nil {
		var username, passwordHash string
		err := a.db.QueryRow(r.Context(),
			`SELECT username,password_hash FROM admin_profiles WHERE profile_key='primary'`).
			Scan(&username, &passwordHash)
		if err == nil {
			return secureStringEqual(name, username) &&
				bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) == nil, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	return secureStringEqual(name, a.cfg.adminUsername) && a.cfg.adminPassword != "" &&
		secureStringEqual(password, a.cfg.adminPassword), nil
}

func (a *API) ensureAdminProfile(r *http.Request) (string, string, string, time.Time, error) {
	name := adminNameFrom(r)
	var username, email, avatar string
	var updated time.Time
	err := a.db.QueryRow(r.Context(), `
		SELECT username,email,avatar_data_url,updated_at
		FROM admin_profiles WHERE profile_key='primary'`).
		Scan(&username, &email, &avatar, &updated)
	if err == nil {
		if !secureStringEqual(name, username) {
			return "", "", "", time.Time{}, pgx.ErrNoRows
		}
		return username, email, avatar, updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", time.Time{}, err
	}
	if !secureStringEqual(name, a.cfg.adminUsername) || a.cfg.adminPassword == "" {
		return "", "", "", time.Time{}, pgx.ErrNoRows
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(a.cfg.adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	_, err = a.db.Exec(r.Context(), `
		INSERT INTO admin_profiles(profile_key,username,email,password_hash)
		VALUES('primary',$1,$2,$3) ON CONFLICT(profile_key) DO NOTHING`,
		name, adminProfileEmail, string(hash))
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	err = a.db.QueryRow(r.Context(), `
		SELECT username,email,avatar_data_url,updated_at
		FROM admin_profiles WHERE profile_key='primary'`).
		Scan(&username, &email, &avatar, &updated)
	if err != nil || !secureStringEqual(name, username) {
		if err == nil {
			err = pgx.ErrNoRows
		}
		return "", "", "", time.Time{}, err
	}
	return username, email, avatar, updated, nil
}

func validAdminAvatar(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(header, ";base64") {
		return "", errors.New("Profile photo must be a base64-encoded PNG, JPEG, or WebP image")
	}
	contentType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if contentType != "image/png" && contentType != "image/jpeg" && contentType != "image/webp" {
		return "", errors.New("Profile photo must be a PNG, JPEG, or WebP image")
	}
	imageData, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(imageData) == 0 || len(imageData) > maxAdminAvatarSize ||
		http.DetectContentType(imageData) != contentType {
		return "", errors.New("Profile photo must be a valid image no larger than 512 KiB")
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(imageData), nil
}

func (a *API) adminProfile(w http.ResponseWriter, r *http.Request) {
	if adminRoleFrom(r) != "admin" {
		writeError(w, http.StatusForbidden, "admin_profile_forbidden", "Only the primary admin can edit this profile")
		return
	}
	username, email, avatar, updated, err := a.ensureAdminProfile(r)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "admin_unauthorized", "The admin session is no longer valid")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin profile")
		return
	}
	var ipAddress string
	err = a.db.QueryRow(r.Context(), `
		SELECT COALESCE(source_ip::text,'') FROM request_logs
		WHERE actor_type='admin' AND actor_id=$1 AND source_ip IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, username).Scan(&ipAddress)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin activity")
		return
	}
	if ipAddress == "" {
		ipAddress = requestSourceIP(r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": username, "email": email, "avatarDataUrl": avatar,
		"updatedAt": updated, "platform": requestUserAgent(r), "serverPlatform": runtimePlatform(),
		"ipAddress": ipAddress,
	})
}

func (a *API) adminProfileRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.adminProfile(w, r)
	case http.MethodPatch:
		a.updateAdminProfile(w, r)
	default:
		methodNotAllowed(w, "GET, PATCH")
	}
}

func (a *API) updateAdminProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username      string `json:"username"`
		AvatarDataURL string `json:"avatarDataUrl"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if !adminUsernamePattern.MatchString(input.Username) {
		writeError(w, http.StatusBadRequest, "invalid_admin_profile", "Username must be 2 to 64 letters, numbers, dots, dashes, or underscores")
		return
	}
	avatar, err := validAdminAvatar(input.AvatarDataURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_admin_avatar", err.Error())
		return
	}
	if _, _, _, _, err := a.ensureAdminProfile(r); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "admin_unauthorized", "The admin session is no longer valid")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update admin profile")
		return
	}
	var username, email, avatarURL string
	var updated time.Time
	err = a.db.QueryRow(r.Context(), `
		UPDATE admin_profiles SET username=$1,avatar_data_url=$2,updated_at=NOW()
		WHERE profile_key='primary'
		RETURNING username,email,avatar_data_url,updated_at`, input.Username, avatar).
		Scan(&username, &email, &avatarURL, &updated)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "admin_username_in_use", "That admin username is already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update admin profile")
		return
	}
	now := time.Now().UTC()
	token, err := signAdminToken(username, "admin", a.cfg.jwtSecret, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not refresh admin session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": username, "email": email, "avatarDataUrl": avatarURL,
		"updatedAt": updated, "token": token, "expiresAt": now.Add(adminTokenLifetime),
		"platform": requestUserAgent(r), "serverPlatform": runtimePlatform(),
		"ipAddress": requestSourceIP(r.RemoteAddr),
	})
}

func (a *API) changeAdminPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.NewPassword) < 12 || len(input.NewPassword) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_admin_password", "New admin password must be 12 to 72 bytes")
		return
	}
	if _, _, _, _, err := a.ensureAdminProfile(r); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "admin_unauthorized", "The admin session is no longer valid")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update admin password")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not secure the new admin password")
		return
	}
	tag, err := a.db.Exec(r.Context(), `
		UPDATE admin_profiles SET password_hash=$1,updated_at=NOW()
		WHERE profile_key='primary' AND username=$2`, string(hash), adminNameFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update admin password")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusUnauthorized, "admin_unauthorized", "The admin session is no longer valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}
