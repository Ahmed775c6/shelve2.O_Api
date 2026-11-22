package api

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "Upload exceeds the allowed size or is malformed")
		return
	}
	var kind, filenameSubmitted, uploadID string
	var fileData []byte
	fileFound := false
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_upload", "Upload exceeds the allowed size or is malformed")
			return
		}
		switch part.FormName() {
		case "type":
			value, readErr := io.ReadAll(io.LimitReader(part, 65))
			_ = part.Close()
			if readErr != nil || len(value) > 64 || kind != "" {
				writeError(w, http.StatusBadRequest, "invalid_upload", "Upload type is invalid")
				return
			}
			kind = strings.TrimSpace(string(value))
		case "uploadId":
			value, readErr := io.ReadAll(io.LimitReader(part, 65))
			_ = part.Close()
			if readErr != nil || len(value) > 64 || uploadID != "" {
				writeError(w, http.StatusBadRequest, "invalid_upload", "Upload id is invalid")
				return
			}
			uploadID = strings.TrimSpace(string(value))
		case "file":
			if fileFound {
				_ = part.Close()
				writeError(w, http.StatusBadRequest, "invalid_upload", "Only one file may be uploaded")
				return
			}
			fileFound = true
			filenameSubmitted = part.FileName()
			data, readErr := io.ReadAll(io.LimitReader(part, maxUpload+1))
			_ = part.Close()
			if readErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_upload", "Could not read uploaded file")
				return
			}
			if len(data) > maxUpload {
				writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "File must be no larger than 20 MiB")
				return
			}
			fileData = data
		default:
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_upload", "Unexpected multipart field")
			return
		}
	}
	if !fileFound || filenameSubmitted == "" || len(fileData) == 0 || !validUUID(uploadID) {
		writeError(w, http.StatusBadRequest, "invalid_upload", "A non-empty file and valid uploadId are required")
		return
	}
	extension := strings.ToLower(filepath.Ext(filepath.Base(filenameSubmitted)))
	if !validUploadType(kind, extension) {
		writeError(w, http.StatusBadRequest, "invalid_upload", "Unsupported file type")
		return
	}
	headLen := len(fileData)
	if headLen > 512 {
		headLen = 512
	}
	detected := http.DetectContentType(fileData[:headLen])
	if !validDetectedType(extension, detected) && !(extension == ".mobi" && validMobi(fileData)) {
		writeError(w, http.StatusBadRequest, "invalid_upload", "File content does not match its extension")
		return
	}
	var existingFilename, existingContentType string
	var existingSize int64
	err = a.db.QueryRow(r.Context(),
		`SELECT filename,content_type,size_bytes FROM files WHERE owner_id=$1 AND kind=$2 AND upload_id=$3`,
		userIDFrom(r), kind, uploadID).Scan(&existingFilename, &existingContentType, &existingSize)
	if err == nil {
		existingPath, pathErr := safeFilePath(a.cfg.uploadDir, kind, existingFilename)
		if pathErr == nil && ensureRegularFile(existingPath) == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"filename": existingFilename, "type": kind, "url": "/api/files/" + kind + "/" + existingFilename,
				"size": existingSize, "contentType": existingContentType,
			})
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not check upload status")
		return
	}
	filename := uploadID + extension
	directory := filepath.Join(a.cfg.uploadDir, kind)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not store file")
		return
	}
	path, err := safeFilePath(a.cfg.uploadDir, kind, filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_upload", "Invalid file path")
		return
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not store file")
		return
	}
	size, writeErr := out.Write(fileData)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil || size != len(fileData) {
		_ = os.Remove(path)
		writeError(w, http.StatusBadRequest, "invalid_upload", "Could not store file")
		return
	}
	contentType := mime.TypeByExtension(extension)
	if contentType == "" && extension == ".epub" {
		contentType = "application/epub+zip"
	}
	if contentType == "" && extension == ".mobi" {
		contentType = "application/x-mobipocket-ebook"
	}
	_, err = a.db.Exec(r.Context(),
		`INSERT INTO files(owner_id,filename,kind,upload_id,content_type,size_bytes,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,NOW())
		 ON CONFLICT(owner_id,kind,upload_id) DO UPDATE SET
		   filename=EXCLUDED.filename,content_type=EXCLUDED.content_type,size_bytes=EXCLUDED.size_bytes,created_at=NOW()`,
		userIDFrom(r), filename, kind, uploadID, contentType, size)
	if err != nil {
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not register file")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": filename, "type": kind, "url": "/api/files/" + kind + "/" + filename,
		"size": size, "contentType": contentType,
	})
}

func (a *API) cleanupOrphanFiles(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT f.filename,f.kind FROM files f
		WHERE f.owner_id=$1 AND f.created_at < NOW()-INTERVAL '7 days'
		  AND NOT EXISTS (
		    SELECT 1 FROM records r
		    WHERE r.owner_id=f.owner_id AND r.deleted_at IS NULL
		      AND r.data::text LIKE '%' || f.filename || '%'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM sync_conflicts c
		    WHERE c.owner_id=f.owner_id AND c.resolved_at IS NULL
		      AND (c.local_data::text LIKE '%' || f.filename || '%'
		        OR c.remote_data::text LIKE '%' || f.filename || '%')
		  )`,
		userIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not find abandoned files")
		return
	}
	type orphan struct{ filename, kind string }
	var orphans []orphan
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.filename, &item.kind); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not read abandoned files")
			return
		}
		orphans = append(orphans, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not read abandoned files")
		return
	}
	rows.Close()
	removed, failures := 0, 0
	for _, item := range orphans {
		path, err := safeFilePath(a.cfg.uploadDir, item.kind, item.filename)
		if err != nil {
			failures++
			log.Printf("could not resolve orphan upload path for %q: %v", item.filename, err)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures++
			log.Printf("could not remove orphan upload %q: %v", item.filename, err)
			continue
		}
		tag, err := a.db.Exec(r.Context(),
			`DELETE FROM files WHERE owner_id=$1 AND filename=$2 AND kind=$3`,
			userIDFrom(r), item.filename, item.kind)
		if err == nil && tag.RowsAffected() == 1 {
			removed++
		} else if err != nil {
			failures++
			log.Printf("could not remove orphan upload metadata for %q: %v", item.filename, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": removed, "failed": failures})
}

func (a *API) download(w http.ResponseWriter, r *http.Request) {
	kind, filename := r.PathValue("type"), r.PathValue("filename")
	path, err := safeFilePath(a.cfg.uploadDir, kind, filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", "Invalid file path")
		return
	}
	var contentType string
	err = a.db.QueryRow(r.Context(),
		`SELECT content_type FROM files WHERE owner_id=$1 AND filename=$2 AND kind=$3`,
		userIDFrom(r), filename, kind).Scan(&contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load file")
		return
	}
	if err := ensureRegularFile(path); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}
