package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *API) books(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listDocuments(w, r, "books")
	case http.MethodPost:
		a.createDocument(w, r, "books", http.StatusCreated)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) bookByID(w http.ResponseWriter, r *http.Request) {
	a.documentItem(w, r, "books", r.PathValue("id"))
}

func (a *API) resourceCollection(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collection := apiResources[resource]
		switch r.Method {
		case http.MethodGet:
			a.listDocuments(w, r, collection)
		case http.MethodPost:
			a.createDocument(w, r, collection, http.StatusCreated)
		default:
			methodNotAllowed(w, "GET, POST")
		}
	}
}

func (a *API) resourceItem(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.documentItem(w, r, apiResources[resource], r.PathValue("id"))
	}
}

func allowedCollection(collection string) bool {
	switch collection {
	case "books", "notes", "writings", "readingPlans", "readingSessions", "writingSessions", "progress":
		return true
	default:
		return false
	}
}

func (a *API) listDocuments(w http.ResponseWriter, r *http.Request, collection string) {
	if !allowedCollection(collection) {
		writeError(w, http.StatusBadRequest, "invalid_record", "Unknown collection")
		return
	}
	query := `SELECT data,updated_at,deleted_at FROM records WHERE owner_id=$1 AND collection=$2 AND deleted_at IS NULL`
	args := []any{userIDFrom(r), collection}
	if collection == "books" {
		if status := r.URL.Query().Get("status"); status != "" {
			if !validBookStatus(status) {
				writeError(w, http.StatusBadRequest, "invalid_status", "status is not supported")
				return
			}
			query += ` AND data->>'status'=$3`
			args = append(args, status)
		}
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load records")
		return
	}
	defer rows.Close()
	response := []map[string]any{}
	for rows.Next() {
		var raw []byte
		var updated time.Time
		var deleted *time.Time
		if err := rows.Scan(&raw, &updated, &deleted); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load records")
			return
		}
		doc := map[string]any{}
		if json.Unmarshal(raw, &doc) != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode record")
			return
		}
		doc["updatedAt"] = updated
		response = append(response, publicDocument(doc))
	}
	writeJSON(w, http.StatusOK, response)
}

func publicDocument(doc map[string]any) map[string]any {
	result := make(map[string]any, len(doc))
	for key, value := range doc {
		if key != "_id" && key != "ownerId" && key != "owner_id" && key != "userId" {
			result[key] = value
		}
	}
	return result
}

func (a *API) publicWritings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT data,updated_at FROM records
		WHERE collection='writings' AND deleted_at IS NULL
		  AND data->>'visibility'='public'
		ORDER BY COALESCE(data->>'publishedAt','') DESC LIMIT 100`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public writings")
		return
	}
	defer rows.Close()
	response := []map[string]any{}
	fields := []string{"id", "title", "subtitle", "genre", "coverColor", "coverImage", "chapters", "wordCount", "publishedAt", "authorName", "synopsis"}
	for rows.Next() {
		var raw []byte
		var updated time.Time
		if err := rows.Scan(&raw, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public writings")
			return
		}
		doc := map[string]any{}
		if json.Unmarshal(raw, &doc) != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode public writing")
			return
		}
		item := map[string]any{}
		for _, field := range fields {
			if value, exists := doc[field]; exists {
				item[field] = value
			}
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) createDocument(w http.ResponseWriter, r *http.Request, collection string, status int) {
	doc, ok := readObject(w, r)
	if !ok {
		return
	}
	delete(doc, "_id")
	delete(doc, "ownerId")
	delete(doc, "owner_id")
	delete(doc, "userId")
	delete(doc, "deletedAt")
	id, exists := doc["id"].(string)
	if exists {
		if !validUUID(id) {
			writeError(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
			return
		}
	} else {
		id = newUUID()
		doc["id"] = id
	}
	if err := validateDocument(doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_record", err.Error())
		return
	}
	if err := validateRequiredFields(collection, doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_record", err.Error())
		return
	}
	if err := a.validateReferences(r.Context(), userIDFrom(r), collection, doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
		return
	}
	now := time.Now().UTC()
	doc["createdAt"] = now
	doc["updatedAt"] = now
	raw, err := json.Marshal(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not encode record")
		return
	}
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO records(owner_id,collection,id,data,updated_at) VALUES($1,$2,$3,$4,$5)
		 ON CONFLICT(owner_id,collection,id) DO NOTHING`,
		userIDFrom(r), collection, id, raw, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not save record")
		return
	}
	writeJSON(w, status, publicDocument(doc))
}

func (a *API) documentItem(w http.ResponseWriter, r *http.Request, collection, id string) {
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
		return
	}
	switch r.Method {
	case http.MethodGet:
		doc, updated, deleted, err := a.loadRecord(r.Context(), userIDFrom(r), collection, id)
		if errors.Is(err, pgx.ErrNoRows) || deleted != nil {
			writeError(w, http.StatusNotFound, "not_found", "Record not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load record")
			return
		}
		doc["updatedAt"] = updated
		writeJSON(w, http.StatusOK, publicDocument(doc))
	case http.MethodPatch:
		updates, ok := readObject(w, r)
		if !ok {
			return
		}
		for _, key := range []string{"id", "_id", "ownerId", "userId", "createdAt", "updatedAt", "deletedAt"} {
			delete(updates, key)
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "empty_update", "At least one editable field is required")
			return
		}
		doc, _, deleted, err := a.loadRecord(r.Context(), userIDFrom(r), collection, id)
		if errors.Is(err, pgx.ErrNoRows) || deleted != nil {
			writeError(w, http.StatusNotFound, "not_found", "Record not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load record")
			return
		}
		for key, value := range updates {
			doc[key] = value
		}
		if err := validateDocument(doc); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_record", err.Error())
			return
		}
		if err := a.validateReferences(r.Context(), userIDFrom(r), collection, doc); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
			return
		}
		now := time.Now().UTC()
		doc["updatedAt"] = now
		raw, _ := json.Marshal(doc)
		tag, err := a.db.Exec(r.Context(),
			`UPDATE records SET data=$1,updated_at=$2,deleted_at=NULL
			 WHERE owner_id=$3 AND collection=$4 AND id=$5 AND deleted_at IS NULL`,
			raw, now, userIDFrom(r), collection, id)
		if err != nil || tag.RowsAffected() != 1 {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not update record")
			return
		}
		writeJSON(w, http.StatusOK, publicDocument(doc))
	case http.MethodDelete:
		now := time.Now().UTC()
		tag, err := a.db.Exec(r.Context(),
			`UPDATE records SET deleted_at=$1,updated_at=$1
			 WHERE owner_id=$2 AND collection=$3 AND id=$4 AND deleted_at IS NULL`,
			now, userIDFrom(r), collection, id)
		if err != nil || tag.RowsAffected() != 1 {
			writeError(w, http.StatusNotFound, "not_found", "Record not found")
			return
		}
		if collection == "books" {
			a.softDeleteBookChildren(r.Context(), userIDFrom(r), id, now)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	default:
		methodNotAllowed(w, "GET, PATCH, DELETE")
	}
}

func (a *API) loadRecord(ctx context.Context, owner, collection, id string) (map[string]any, time.Time, *time.Time, error) {
	var raw []byte
	var updated time.Time
	var deleted *time.Time
	err := a.db.QueryRow(ctx,
		`SELECT data,updated_at,deleted_at FROM records WHERE owner_id=$1 AND collection=$2 AND id=$3`,
		owner, collection, id).Scan(&raw, &updated, &deleted)
	if err != nil {
		return nil, updated, deleted, err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, updated, deleted, err
	}
	return doc, updated, deleted, nil
}

func (a *API) softDeleteBookChildren(ctx context.Context, owner, bookID string, now time.Time) {
	_, _ = a.db.Exec(ctx,
		`UPDATE records SET deleted_at=$1,updated_at=$1
		 WHERE owner_id=$2 AND (collection='notes' OR collection='readingSessions' OR collection='progress')
		   AND data->>'bookId'=$3`,
		now, owner, bookID)
	_, _ = a.db.Exec(ctx,
		`UPDATE records SET data=JSONB_SET(data,'{bookIds}',COALESCE(
		   (SELECT jsonb_agg(value) FROM jsonb_array_elements(data->'bookIds') value WHERE value<>to_jsonb($3::text)), '[]'::jsonb)),
		   updated_at=$1
		 WHERE owner_id=$2 AND collection='readingPlans' AND deleted_at IS NULL`,
		now, owner, bookID)
}

func (a *API) ownsBook(ctx context.Context, owner, bookID string) bool {
	if !validUUID(bookID) {
		return false
	}
	var exists bool
	err := a.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM records WHERE owner_id=$1 AND collection='books' AND id=$2 AND deleted_at IS NULL)`,
		owner, bookID).Scan(&exists)
	return err == nil && exists
}

func (a *API) validateReferences(ctx context.Context, owner, collection string, doc map[string]any) error {
	if deleted, ok := doc["deletedAt"].(string); ok && deleted != "" {
		return nil
	}
	switch collection {
	case "notes", "readingSessions":
		bookID, _ := doc["bookId"].(string)
		if !a.ownsBook(ctx, owner, bookID) {
			return errors.New("record must reference a book owned by this user")
		}
	case "progress":
		bookID, _ := doc["bookId"].(string)
		if bookID == "" {
			bookID, _ = doc["id"].(string)
		}
		if !a.ownsBook(ctx, owner, bookID) {
			return errors.New("progress must reference a book owned by this user")
		}
	case "readingPlans":
		ids, ok := doc["bookIds"].([]any)
		if !ok {
			return nil
		}
		seen := map[string]struct{}{}
		if len(ids) > 500 {
			return errors.New("reading plans must contain at most 500 bookIds")
		}
		for _, raw := range ids {
			id, ok := raw.(string)
			if !ok || !a.ownsBook(ctx, owner, id) {
				return errors.New("reading plans may only reference books owned by this user")
			}
			if _, duplicate := seen[id]; duplicate {
				return errors.New("reading plans cannot contain duplicate bookIds")
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func (a *API) bookNotes(w http.ResponseWriter, r *http.Request) {
	bookID := r.PathValue("id")
	if !validUUID(bookID) || !a.ownsBook(r.Context(), userIDFrom(r), bookID) {
		writeError(w, http.StatusNotFound, "not_found", "Book not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.listFilteredRecords(w, r, "notes", "data->>'bookId'=$3", bookID)
	case http.MethodPost:
		doc, ok := readObject(w, r)
		if !ok {
			return
		}
		doc["bookId"] = bookID
		a.createDocument(w, r, "notes", http.StatusCreated)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) listFilteredRecords(w http.ResponseWriter, r *http.Request, collection, predicate string, value string) {
	rows, err := a.db.Query(r.Context(),
		`SELECT data,updated_at FROM records WHERE owner_id=$1 AND collection=$2 AND deleted_at IS NULL AND `+predicate+` ORDER BY updated_at DESC`,
		userIDFrom(r), collection, value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load records")
		return
	}
	defer rows.Close()
	response := []map[string]any{}
	for rows.Next() {
		var raw []byte
		var updated time.Time
		if err := rows.Scan(&raw, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load records")
			return
		}
		doc := map[string]any{}
		if json.Unmarshal(raw, &doc) != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode records")
			return
		}
		doc["updatedAt"] = updated
		response = append(response, publicDocument(doc))
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) progress(w http.ResponseWriter, r *http.Request) {
	bookID := r.PathValue("bookId")
	if !validUUID(bookID) || !a.ownsBook(r.Context(), userIDFrom(r), bookID) {
		writeError(w, http.StatusNotFound, "not_found", "Book not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		doc, updated, deleted, err := a.loadRecord(r.Context(), userIDFrom(r), "progress", bookID)
		if errors.Is(err, pgx.ErrNoRows) || deleted != nil {
			writeJSON(w, http.StatusOK, map[string]any{"bookId": bookID, "currentPage": 0, "updatedAt": nil})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load progress")
			return
		}
		doc["updatedAt"] = updated
		writeJSON(w, http.StatusOK, publicDocument(doc))
	case http.MethodPatch, http.MethodPost:
		input, ok := readObject(w, r)
		if !ok {
			return
		}
		page, valid := numericInt(input["currentPage"])
		if !valid || page < 0 {
			writeError(w, http.StatusBadRequest, "invalid_progress", "currentPage must be a non-negative integer")
			return
		}
		book, _, _, err := a.loadRecord(r.Context(), userIDFrom(r), "books", bookID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Book not found")
			return
		}
		total, hasTotal := numericInt(book["totalPages"])
		if hasTotal && total > 0 && page > total {
			writeError(w, http.StatusBadRequest, "invalid_progress", "currentPage cannot exceed totalPages")
			return
		}
		now := time.Now().UTC()
		status := "reading"
		if hasTotal && total > 0 && page == total {
			status = "archived"
			book["finishedAt"] = now
		}
		book["currentPage"] = page
		book["status"] = status
		book["updatedAt"] = now
		progress := map[string]any{"id": bookID, "bookId": bookID, "currentPage": page, "updatedAt": now}
		tx, err := a.db.Begin(r.Context())
		if err == nil {
			defer tx.Rollback(r.Context())
			bookRaw, _ := json.Marshal(book)
			progressRaw, _ := json.Marshal(progress)
			_, err = tx.Exec(r.Context(), `UPDATE records SET data=$1,updated_at=$2,deleted_at=NULL WHERE owner_id=$3 AND collection='books' AND id=$4`,
				bookRaw, now, userIDFrom(r), bookID)
			if err == nil {
				_, err = tx.Exec(r.Context(),
					`INSERT INTO records(owner_id,collection,id,data,updated_at) VALUES($1,'progress',$2,$3,$4)
					 ON CONFLICT(owner_id,collection,id) DO UPDATE SET data=EXCLUDED.data,updated_at=EXCLUDED.updated_at,deleted_at=NULL`,
					userIDFrom(r), bookID, progressRaw, now)
			}
			if err == nil {
				err = tx.Commit(r.Context())
			}
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save progress")
			return
		}
		writeJSON(w, http.StatusOK, progress)
	default:
		methodNotAllowed(w, "GET, PATCH")
	}
}

func validateDocument(doc map[string]any) error {
	var validate func(any) error
	validate = func(value any) error {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				if key == "" || strings.HasPrefix(key, "$") || strings.Contains(key, ".") ||
					key == "_id" || key == "ownerId" || key == "owner_id" || key == "userId" {
					return errors.New("record contains a reserved field")
				}
				if err := validate(child); err != nil {
					return err
				}
			}
		case []any:
			if len(current) > 10000 {
				return errors.New("record contains an oversized array")
			}
			for _, child := range current {
				if err := validate(child); err != nil {
					return err
				}
			}
		case string:
			if len(current) > 200000 {
				return errors.New("text fields must be no larger than 200000 bytes")
			}
		}
		return nil
	}
	if err := validate(doc); err != nil {
		return err
	}
	if title, exists := doc["title"]; exists {
		value, ok := title.(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 500 {
			return errors.New("title must be a non-empty string of at most 500 characters")
		}
	}
	if status, exists := doc["status"]; exists {
		value, ok := status.(string)
		if !ok || !validBookStatus(value) {
			return errors.New("status is not supported")
		}
	}
	if author, exists := doc["author"]; exists {
		value, ok := author.(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 300 {
			return errors.New("author must be a non-empty string of at most 300 characters")
		}
	}
	if category, exists := doc["category"]; exists {
		value, ok := category.(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 100 {
			return errors.New("category must be a non-empty string of at most 100 characters")
		}
	}
	if total, exists := doc["totalPages"]; exists {
		value, ok := numericInt(total)
		if !ok || value < 0 || value > 10_000_000 {
			return errors.New("totalPages must be a non-negative integer")
		}
	}
	if page, exists := doc["currentPage"]; exists {
		value, ok := numericInt(page)
		if !ok || value < 0 {
			return errors.New("currentPage must be a non-negative integer")
		}
	}
	if rating, exists := doc["rating"]; exists {
		value, ok := rating.(float64)
		if !ok || value < 0 || value > 5 {
			return errors.New("rating must be between 0 and 5")
		}
	}
	if tags, exists := doc["tags"]; exists {
		values, ok := tags.([]any)
		if !ok || len(values) > 100 {
			return errors.New("tags must be an array of at most 100 strings")
		}
		for _, tag := range values {
			value, ok := tag.(string)
			if !ok || len(value) > 100 {
				return errors.New("tags must be an array of at most 100 strings")
			}
		}
	}
	if reminder, exists := doc["reminderEnabled"]; exists {
		if _, ok := reminder.(bool); !ok {
			return errors.New("reminderEnabled must be a boolean")
		}
	}
	if days, exists := doc["daysOfWeek"]; exists {
		values, ok := days.([]any)
		if !ok || len(values) > 7 {
			return errors.New("daysOfWeek must contain weekday numbers from 0 to 6")
		}
		seen := map[string]struct{}{}
		for _, day := range values {
			dayKey := ""
			if label, ok := day.(string); ok {
				if !containsString([]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}, label) {
					return errors.New("daysOfWeek must contain weekday labels")
				}
				dayKey = label
			} else if value, ok := numericInt(day); ok && value >= 0 && value <= 6 {
				dayKey = strconv.FormatInt(value, 10)
			} else {
				return errors.New("daysOfWeek must contain weekday labels or numbers from 0 to 6")
			}
			if _, duplicate := seen[dayKey]; duplicate {
				return errors.New("daysOfWeek cannot contain duplicate weekdays")
			}
			seen[dayKey] = struct{}{}
		}
	}
	return nil
}

func validateRequiredFields(collection string, doc map[string]any) error {
	switch collection {
	case "books":
		for _, field := range []string{"title", "author"} {
			value, ok := doc[field].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", field)
			}
		}
	case "readingPlans":
		for _, field := range []string{"title", "startDate", "endDate", "time"} {
			value, ok := doc[field].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", field)
			}
		}
		if _, ok := doc["bookIds"].([]any); !ok {
			return errors.New("bookIds is required")
		}
		if _, ok := doc["daysOfWeek"].([]any); !ok {
			return errors.New("daysOfWeek is required")
		}
		if _, ok := doc["reminderEnabled"].(bool); !ok {
			return errors.New("reminderEnabled is required")
		}
	case "readingSessions", "notes":
		if _, ok := doc["bookId"].(string); !ok {
			return errors.New("bookId is required")
		}
	case "progress":
		if _, ok := doc["currentPage"]; !ok {
			return errors.New("currentPage is required")
		}
	}
	return nil
}

func validBookStatus(value string) bool {
	return value == "unread" || value == "reading" || value == "archived"
}
