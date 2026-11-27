package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type adminNoteInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func adminNoteOwner(r *http.Request) string {
	if adminRoleFrom(r) == "admin" {
		return "primary"
	}
	return "developer:" + adminNameFrom(r)
}

func validateAdminNote(input *adminNoteInput) bool {
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	return input.Title != "" && len(input.Title) <= 120 && input.Content != "" && len(input.Content) <= 20000
}

func adminNoteJSON(id, title, content string, createdAt, updatedAt time.Time) map[string]any {
	return map[string]any{
		"id": id, "title": title, "content": content,
		"createdAt": createdAt, "updatedAt": updatedAt,
	}
}

func (a *API) adminNotes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.Query(r.Context(), `
			SELECT id::text,title,content,created_at,updated_at
			FROM admin_notes WHERE admin_name=$1 ORDER BY updated_at DESC,id DESC`,
			adminNoteOwner(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin notes")
			return
		}
		defer rows.Close()
		notes := make([]map[string]any, 0)
		for rows.Next() {
			var id, title, content string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&id, &title, &content, &createdAt, &updatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin notes")
				return
			}
			notes = append(notes, adminNoteJSON(id, title, content, createdAt, updatedAt))
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin notes")
			return
		}
		writeJSON(w, http.StatusOK, notes)
	case http.MethodPost:
		var input adminNoteInput
		if !decodeJSON(w, r, &input) {
			return
		}
		if !validateAdminNote(&input) {
			writeError(w, http.StatusBadRequest, "invalid_note", "A note title up to 120 characters and content up to 20000 characters are required")
			return
		}
		var id string
		var createdAt, updatedAt time.Time
		err := a.db.QueryRow(r.Context(), `
			INSERT INTO admin_notes(id,admin_name,title,content)
			VALUES($1,$2,$3,$4)
			RETURNING id::text,created_at,updated_at`,
			newUUID(), adminNoteOwner(r), input.Title, input.Content).Scan(&id, &createdAt, &updatedAt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save admin note")
			return
		}
		writeJSON(w, http.StatusCreated, adminNoteJSON(id, input.Title, input.Content, createdAt, updatedAt))
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) adminNoteByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Note id must be a UUID")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input adminNoteInput
		if !decodeJSON(w, r, &input) {
			return
		}
		if !validateAdminNote(&input) {
			writeError(w, http.StatusBadRequest, "invalid_note", "A note title up to 120 characters and content up to 20000 characters are required")
			return
		}
		var createdAt, updatedAt time.Time
		err := a.db.QueryRow(r.Context(), `
			UPDATE admin_notes SET title=$1,content=$2,updated_at=NOW()
			WHERE id=$3 AND admin_name=$4
			RETURNING created_at,updated_at`,
			input.Title, input.Content, id, adminNoteOwner(r)).Scan(&createdAt, &updatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Admin note not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not update admin note")
			return
		}
		writeJSON(w, http.StatusOK, adminNoteJSON(id, input.Title, input.Content, createdAt, updatedAt))
	case http.MethodDelete:
		tag, err := a.db.Exec(r.Context(), `DELETE FROM admin_notes WHERE id=$1 AND admin_name=$2`, id, adminNoteOwner(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete admin note")
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, http.StatusNotFound, "not_found", "Admin note not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w, "PATCH, DELETE")
	}
}
