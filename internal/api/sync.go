package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"time"
)

func newerTimestampWins(incoming, existing time.Time) bool {
	return incoming.After(existing)
}

func (a *API) importJSON(w http.ResponseWriter, r *http.Request) {
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	counts := make(map[string]map[string]int)
	for _, resource := range syncResourceOrder {
		collection := syncResources[resource]
		items, exists := body[resource]
		if !exists {
			continue
		}
		records, valid := items.([]any)
		if !valid || len(records) > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_import", resource+" must be an array of at most 1000 records")
			return
		}
		counts[resource] = map[string]int{"inserted": 0, "updated": 0, "skipped": 0, "conflict": 0}
		for _, item := range records {
			record, ok := item.(map[string]any)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_import", "Every imported record must be an object")
				return
			}
			outcome, err := a.mergeRecord(r.Context(), userIDFrom(r), collection, record)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_import", err.Error())
				return
			}
			counts[resource][outcome]++
		}
	}
	conflictCount := 0
	for _, outcomes := range counts {
		conflictCount += outcomes["conflict"]
	}
	if conflictCount > 0 {
		now := time.Now().UTC()
		if err := a.sendOptInNotification(
			r.Context(),
			userIDFrom(r),
			"sync-conflict",
			now.Format("2006-01-02"),
			"Shelve: resolve a library sync conflict",
			fmt.Sprintf("Shelve found %d conflicting library change(s). Open Shelve and choose which version to keep.", conflictCount),
		); err != nil {
			log.Printf("sync-conflict notification email failed: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": counts})
}

func (a *API) replaceAllData(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string         `json:"password"`
		Data     map[string]any `json:"data"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Password == "" || input.Data == nil {
		writeError(w, http.StatusBadRequest, "invalid_replacement", "Password and replacement data are required")
		return
	}

	type replacementRecord struct {
		collection string
		id         string
		data       []byte
		updatedAt  time.Time
		deletedAt  *time.Time
	}
	records := make([]replacementRecord, 0)
	activeBooks := make(map[string]struct{})
	seen := make(map[string]struct{})
	for _, resource := range syncResourceOrder {
		collection := syncResources[resource]
		items, ok := input.Data[resource].([]any)
		if !ok || len(items) > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_replacement", resource+" must be an array of at most 1000 records")
			return
		}
		for _, item := range items {
			record, ok := item.(map[string]any)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Every replacement record must be an object")
				return
			}
			if err := validateDocument(record); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", err.Error())
				return
			}
			if err := validateRequiredFields(collection, record); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", err.Error())
				return
			}
			id, ok := record["id"].(string)
			if !ok || !validUUID(id) {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Every replacement record must have a valid UUID id")
				return
			}
			key := id
			if collection == "progress" {
				if bookID, ok := record["bookId"].(string); ok && bookID != "" {
					key = bookID
				}
			}
			if !validUUID(key) {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "A replacement record references an invalid id")
				return
			}
			uniqueKey := collection + "\x00" + key
			if _, duplicate := seen[uniqueKey]; duplicate {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Replacement data contains duplicate records")
				return
			}
			seen[uniqueKey] = struct{}{}
			updatedAt, err := parseTimestamp(record["updatedAt"])
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Every replacement record must have a valid updatedAt timestamp")
				return
			}
			deletedAt := parseDeletedAt(record)
			if rawDeleted, exists := record["deletedAt"]; exists && rawDeleted != nil && deletedAt == nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "deletedAt must be an RFC3339 timestamp")
				return
			}
			raw, err := json.Marshal(record)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Could not encode replacement record")
				return
			}
			records = append(records, replacementRecord{
				collection: collection, id: key, data: raw, updatedAt: updatedAt, deletedAt: deletedAt,
			})
			if collection == "books" && deletedAt == nil {
				activeBooks[id] = struct{}{}
			}
		}
	}
	for _, record := range records {
		if record.deletedAt != nil {
			continue
		}
		var bookID string
		switch record.collection {
		case "notes", "readingSessions", "progress":
			var data map[string]any
			if err := json.Unmarshal(record.data, &data); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Could not decode replacement record")
				return
			}
			bookID, _ = data["bookId"].(string)
			if bookID == "" && record.collection == "progress" {
				bookID = record.id
			}
		case "readingPlans":
			var data map[string]any
			if err := json.Unmarshal(record.data, &data); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Could not decode replacement record")
				return
			}
			bookIDs, _ := data["bookIds"].([]any)
			for _, value := range bookIDs {
				id, ok := value.(string)
				if !ok {
					writeError(w, http.StatusBadRequest, "invalid_replacement", "Reading plans may only reference books in the replacement data")
					return
				}
				if _, exists := activeBooks[id]; !exists {
					writeError(w, http.StatusBadRequest, "invalid_replacement", "Reading plans may only reference books in the replacement data")
					return
				}
			}
		}
		if bookID != "" {
			if _, exists := activeBooks[bookID]; !exists {
				writeError(w, http.StatusBadRequest, "invalid_replacement", "Records may only reference books in the replacement data")
				return
			}
		}
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not replace cloud data")
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
	if _, err := tx.Exec(r.Context(), `DELETE FROM sync_conflicts WHERE owner_id=$1`, userIDFrom(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not clear cloud sync conflicts")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM records WHERE owner_id=$1`, userIDFrom(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not clear cloud records")
		return
	}
	for _, record := range records {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO records(owner_id,collection,id,data,updated_at,deleted_at)
			 VALUES($1,$2,$3,$4,$5,$6)`,
			userIDFrom(r), record.collection, record.id, record.data, record.updatedAt, record.deletedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save replacement cloud data")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not commit replacement cloud data")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replaced": true, "records": len(records)})
}

func (a *API) pull(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if rawSince := r.URL.Query().Get("since"); rawSince != "" {
		parsed, err := time.Parse(time.RFC3339Nano, rawSince)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since", "since must be an RFC3339 timestamp")
			return
		}
		since = parsed
	}
	result := make(map[string][]map[string]any)
	for resource, collection := range syncResources {
		query := `SELECT data,updated_at,deleted_at FROM records WHERE owner_id=$1 AND collection=$2`
		args := []any{userIDFrom(r), collection}
		if !since.IsZero() {
			query += ` AND updated_at>$3`
			args = append(args, since)
		}
		query += ` ORDER BY updated_at ASC`
		rows, err := a.db.Query(r.Context(), query, args...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not pull records")
			return
		}
		values := []map[string]any{}
		for rows.Next() {
			var raw []byte
			var updated time.Time
			var deleted *time.Time
			if err := rows.Scan(&raw, &updated, &deleted); err != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not pull records")
				return
			}
			doc := map[string]any{}
			if json.Unmarshal(raw, &doc) != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode records")
				return
			}
			doc["updatedAt"] = updated
			if deleted != nil {
				doc["deletedAt"] = deleted
			}
			values = append(values, publicDocument(doc))
		}
		rows.Close()
		result[resource] = values
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) syncConflicts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.Query(r.Context(),
			`SELECT id::text,collection,record_id::text,local_data,remote_data,created_at,resolved_at
			 FROM sync_conflicts WHERE owner_id=$1 AND resolved_at IS NULL ORDER BY created_at DESC`,
			userIDFrom(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load sync conflicts")
			return
		}
		defer rows.Close()
		values := []map[string]any{}
		for rows.Next() {
			var id, collection, recordID string
			var local, remote []byte
			var created time.Time
			var resolved *time.Time
			if err := rows.Scan(&id, &collection, &recordID, &local, &remote, &created, &resolved); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not load sync conflicts")
				return
			}
			values = append(values, map[string]any{
				"id": id, "collection": collection, "recordId": recordID,
				"localData": json.RawMessage(local), "remoteData": json.RawMessage(remote),
				"createdAt": created,
			})
		}
		writeJSON(w, http.StatusOK, values)
	case http.MethodPost:
		body, ok := readObject(w, r)
		if !ok {
			return
		}
		id, _ := body["id"].(string)
		if !validUUID(id) {
			writeError(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
			return
		}
		resolution, _ := body["resolution"].(string)
		if resolution != "keep_mine" && resolution != "accept_remote" && resolution != "keep_both" {
			writeError(w, http.StatusBadRequest, "invalid_resolution", "Choose keep_mine, accept_remote, or keep_both")
			return
		}
		tx, err := a.db.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not resolve conflict")
			return
		}
		defer tx.Rollback(r.Context())
		var collection, recordID string
		var localRaw, remoteRaw []byte
		err = tx.QueryRow(r.Context(),
			`SELECT collection,record_id::text,local_data,remote_data FROM sync_conflicts
			 WHERE id=$1 AND owner_id=$2 AND resolved_at IS NULL FOR UPDATE`,
			id, userIDFrom(r)).Scan(&collection, &recordID, &localRaw, &remoteRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Conflict not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load conflict")
			return
		}
		var selected, canonical, conflictCopy map[string]any
		if resolution == "accept_remote" {
			if err := json.Unmarshal(remoteRaw, &selected); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Stored remote conflict data is invalid")
				return
			}
			canonical = selected
		} else {
			if err := json.Unmarshal(localRaw, &selected); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Stored local conflict data is invalid")
				return
			}
			now := time.Now().UTC()
			if resolution == "keep_both" {
				if err := json.Unmarshal(remoteRaw, &canonical); err != nil {
					writeError(w, http.StatusInternalServerError, "internal_error", "Stored remote conflict data is invalid")
					return
				}
				selected["id"] = newUUID()
				selected["createdAt"] = now
				if title, ok := selected["title"].(string); ok {
					selected["title"] = title + " (Conflict copy)"
				}
				conflictCopy = selected
			} else {
				canonical = selected
			}
			selected["updatedAt"] = now
			delete(selected, "ownerId")
			selectedID, _ := selected["id"].(string)
			if resolution == "keep_both" {
				copyRaw, _ := json.Marshal(conflictCopy)
				_, err = tx.Exec(r.Context(),
					`INSERT INTO records(owner_id,collection,id,data,updated_at,deleted_at)
					 VALUES($1,$2,$3,$4,$5,$6)`,
					userIDFrom(r), collection, selectedID, copyRaw, now, parseDeletedAt(conflictCopy))
			} else {
				canonical["updatedAt"] = now
				canonicalRaw, _ := json.Marshal(canonical)
				_, err = tx.Exec(r.Context(),
					`UPDATE records SET data=$1,updated_at=$2,deleted_at=$3
					 WHERE owner_id=$4 AND collection=$5 AND id=$6`,
					canonicalRaw, now, parseDeletedAt(canonical), userIDFrom(r), collection, recordID)
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not save conflict resolution")
				return
			}
		}
		_, err = tx.Exec(r.Context(),
			`UPDATE sync_conflicts SET resolved_at=NOW() WHERE id=$1 AND owner_id=$2`,
			id, userIDFrom(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not finalize conflict resolution")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not commit conflict resolution")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"resolved": true, "resolution": resolution,
			"record": selected, "canonicalRecord": canonical, "conflictCopy": conflictCopy,
		})
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) mergeRecord(ctx context.Context, owner, collection string, record map[string]any) (string, error) {
	id, ok := record["id"].(string)
	if !ok || !validUUID(id) {
		return "", errors.New("each imported record must have a valid UUID id")
	}
	if err := validateDocument(record); err != nil {
		return "", err
	}
	if err := validateRequiredFields(collection, record); err != nil {
		return "", err
	}
	incomingTime, err := parseTimestamp(record["updatedAt"])
	if err != nil {
		return "", errors.New("each imported record must have a valid updatedAt timestamp")
	}
	key := id
	if collection == "progress" {
		key, _ = record["bookId"].(string)
		if key == "" {
			key = id
		}
	}
	if !validUUID(key) {
		return "", errors.New("imported record references an invalid id")
	}
	if err := a.validateReferences(ctx, owner, collection, record); err != nil {
		return "", err
	}

	var existingRaw []byte
	var existingTime time.Time
	var existingDeleted *time.Time
	err = a.db.QueryRow(ctx,
		`SELECT data,updated_at,deleted_at FROM records WHERE owner_id=$1 AND collection=$2 AND id=$3`,
		owner, collection, key).Scan(&existingRaw, &existingTime, &existingDeleted)
	if errors.Is(err, pgx.ErrNoRows) {
		raw, _ := json.Marshal(record)
		_, err = a.db.Exec(ctx,
			`INSERT INTO records(owner_id,collection,id,data,updated_at,deleted_at) VALUES($1,$2,$3,$4,$5,$6)
			 ON CONFLICT(owner_id,collection,id) DO NOTHING`,
			owner, collection, key, raw, incomingTime, parseDeletedAt(record))
		if err != nil {
			return "", errors.New("could not import record")
		}
		return "inserted", nil
	}
	if err != nil {
		return "", errors.New("could not read existing record")
	}
	if !newerTimestampWins(incomingTime, existingTime) {
		if incomingTime.Before(existingTime) {
			existingDoc := map[string]any{}
			if json.Unmarshal(existingRaw, &existingDoc) == nil {
				localRaw, _ := json.Marshal(record)
				remoteRaw, _ := json.Marshal(existingDoc)
				_, _ = a.db.Exec(ctx,
					`INSERT INTO sync_conflicts(id,owner_id,collection,record_id,local_data,remote_data)
					 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`,
					newUUID(), owner, collection, key, localRaw, remoteRaw)
			}
			return "conflict", nil
		}
		return "skipped", nil
	}
	raw, _ := json.Marshal(record)
	tag, err := a.db.Exec(ctx,
		`UPDATE records SET data=$1,updated_at=$2,deleted_at=$3
		 WHERE owner_id=$4 AND collection=$5 AND id=$6 AND updated_at<$2`,
		raw, incomingTime, parseDeletedAt(record), owner, collection, key)
	if err != nil {
		return "", errors.New("could not merge record")
	}
	if tag.RowsAffected() == 0 {
		return "skipped", nil
	}
	return "updated", nil
}

func parseDeletedAt(record map[string]any) *time.Time {
	value, ok := record["deletedAt"].(string)
	if !ok || value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}
