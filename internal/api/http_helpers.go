package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
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
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
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
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, owner)))
	})
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
