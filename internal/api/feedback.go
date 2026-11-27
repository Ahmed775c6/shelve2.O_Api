package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

func detectUserPlatform(userAgent string) string {
	value := strings.ToLower(userAgent)
	switch {
	case strings.Contains(value, "android"):
		return "Android"
	case strings.Contains(value, "iphone") || strings.Contains(value, "ipad") || strings.Contains(value, "ios"):
		return "iOS"
	case strings.Contains(value, "windows"):
		return "Windows"
	case strings.Contains(value, "macintosh") || strings.Contains(value, "mac os"):
		return "macOS"
	case strings.Contains(value, "linux"):
		return "Linux"
	case value == "":
		return "Unknown"
	default:
		return "Other"
	}
}

func (a *API) recordUserActivity(parent context.Context, userID, method, path string, status int, platform string) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if _, err := a.db.Exec(ctx, `UPDATE users SET last_seen_at=NOW(),platform=$2 WHERE id=$1`, userID, platform); err != nil {
		log.Printf("user activity update failed for user %s: %v", userID, err)
		return
	}
	if _, err := a.db.Exec(ctx,
		`INSERT INTO user_activity(user_id,method,path,status,platform) VALUES($1,$2,$3,$4,$5)`,
		userID, method, path, status, platform); err != nil {
		log.Printf("user activity write failed for user %s: %v", userID, err)
	}
}

func (a *API) createUserFeedback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Category = strings.TrimSpace(input.Category)
	input.Message = strings.TrimSpace(input.Message)
	if input.Category == "" || len(input.Category) > 40 || input.Message == "" || len(input.Message) > 4000 {
		writeError(w, http.StatusBadRequest, "invalid_feedback", "Choose a category and provide a message of up to 4000 characters")
		return
	}
	_, err := a.db.Exec(r.Context(),
		`INSERT INTO user_feedback(id,owner_id,category,message) VALUES($1,$2,$3,$4)`,
		newUUID(), userIDFrom(r), input.Category, input.Message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not send your feedback")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"sent": true})
}

func (a *API) adminFeedback(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT f.id::text,f.owner_id::text,u.name,u.email,f.category,f.message,f.status,f.created_at,f.updated_at
		FROM user_feedback f JOIN users u ON u.id=f.owner_id
		ORDER BY CASE f.status WHEN 'new' THEN 0 WHEN 'reviewing' THEN 1 ELSE 2 END,f.created_at DESC
		LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user insights")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, ownerID, name, email, category, message, status string
		var created, updated time.Time
		if err := rows.Scan(&id, &ownerID, &name, &email, &category, &message, &status, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user insights")
			return
		}
		items = append(items, map[string]any{
			"id": id, "userId": ownerID, "userName": name, "userEmail": email,
			"category": category, "message": message, "status": status, "createdAt": created, "updatedAt": updated,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user insights")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) adminFeedbackByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Insight id must be a UUID")
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Status != "new" && input.Status != "reviewing" && input.Status != "resolved" {
		writeError(w, http.StatusBadRequest, "invalid_status", "Status must be new, reviewing, or resolved")
		return
	}
	tag, err := a.db.Exec(r.Context(),
		`UPDATE user_feedback SET status=$1,updated_at=NOW() WHERE id=$2`, input.Status, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update insight")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Insight not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}
