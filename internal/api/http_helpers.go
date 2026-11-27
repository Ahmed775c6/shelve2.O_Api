package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func methodGate(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			methodNotAllowed(w, method)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := a.cfg.origins[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Admin-Password")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) allowRate(r *http.Request, limit int, window time.Duration) bool {
	key := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		key = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	now := time.Now()
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	values := a.rate[key]
	filtered := values[:0]
	for _, timestamp := range values {
		if now.Sub(timestamp) < window {
			filtered = append(filtered, timestamp)
		}
	}
	if len(filtered) >= limit {
		a.rate[key] = filtered
		return false
	}
	a.rate[key] = append(filtered, now)
	return true
}

func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "A bearer token is required")
			return
		}
		owner, err := verifyToken(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer")), a.cfg.jwtSecret, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired bearer token")
			return
		}
		claims, err := verifySignedClaims(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer")), a.cfg.jwtSecret, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired bearer token")
			return
		}
		if a.db != nil {
			var banned bool
			var revokedAt *time.Time
			err := a.db.QueryRow(r.Context(),
				`SELECT banned_at IS NOT NULL,session_revoked_at FROM users WHERE id=$1`, owner).
				Scan(&banned, &revokedAt)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusUnauthorized, "unauthorized", "Account is unavailable")
					return
				}
				writeError(w, http.StatusServiceUnavailable, "service_unavailable", "Could not verify account access")
				return
			}
			if banned {
				writeError(w, http.StatusForbidden, "account_banned", "This account has been suspended")
				return
			}
			if revokedAt != nil && !revokedAt.Before(time.Unix(claims.Issued, 0)) {
				writeError(w, http.StatusUnauthorized, "session_revoked", "This session is no longer valid")
				return
			}
		}
		platform := detectUserPlatform(r.UserAgent())
		ctx := context.WithValue(r.Context(), userContextKey, owner)
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r.WithContext(ctx))
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if a.db != nil {
			a.recordUserActivity(r.Context(), owner, r.Method, r.URL.Path, status, platform)
		}
	})
}

type adminContextKey string

const (
	adminRoleContextKey adminContextKey = "adminRole"
	adminNameContextKey adminContextKey = "adminName"
)

func (a *API) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "admin_unauthorized", "An admin session is required")
			return
		}
		claims, err := verifyAdminToken(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer")), a.cfg.jwtSecret, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "admin_unauthorized", "The admin session is invalid or expired")
			return
		}
		ctx := context.WithValue(r.Context(), adminRoleContextKey, claims.Role)
		ctx = context.WithValue(ctx, adminNameContextKey, claims.AdminName)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) adminReauth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}

		valid, err := a.verifyAdminPassword(r, r.Header.Get("X-Admin-Password"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify admin password")
			return
		}
		if !valid {
			writeError(w, http.StatusUnauthorized, "admin_reauthentication_required", "Enter your current admin password to confirm this change")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminRoleFrom(r *http.Request) string {
	role, _ := r.Context().Value(adminRoleContextKey).(string)
	return role
}

func adminNameFrom(r *http.Request) string {
	name, _ := r.Context().Value(adminNameContextKey).(string)
	return name
}

func secureStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func userIDFrom(r *http.Request) string {
	owner, _ := r.Context().Value(userContextKey).(string)
	return owner
}

func readObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var body map[string]any
	if !decodeJSON(w, r, &body) {
		return nil, false
	}
	if body == nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Expected a JSON object")
		return nil, false
	}
	return body, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request must contain valid JSON no larger than 1 MiB")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request must contain exactly one JSON value")
		return false
	}
	return true
}

func stringField(doc map[string]any, key string) string {
	value, _ := doc[key].(string)
	return value
}

func parseTimestamp(value any) (time.Time, error) {
	switch timestamp := value.(type) {
	case time.Time:
		return timestamp, nil
	case string:
		return time.Parse(time.RFC3339Nano, timestamp)
	default:
		return time.Time{}, errors.New("invalid timestamp")
	}
}

func numericInt(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		if number == float64(int64(number)) {
			return int64(number), true
		}
	case int:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	}
	return 0, false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func methodNotAllowed(w http.ResponseWriter, methods string) {
	w.Header().Set("Allow", methods)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
}
