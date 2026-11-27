package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxErrorReportUpload = 16 << 20
	maxErrorScreenshot   = 5 << 20
	maxErrorScreenshots  = 3
)

type errorReportAttachment struct {
	id          string
	filename    string
	contentType string
	data        []byte
}

type errorReportSubmission struct {
	title       string
	message     string
	details     string
	steps       string
	screen      string
	metadata    map[string]any
	attachments []errorReportAttachment
}

func readErrorReportSubmission(w http.ResponseWriter, r *http.Request) (errorReportSubmission, bool) {
	var submission errorReportSubmission
	submission.metadata = make(map[string]any)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxErrorReportUpload)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			if r.MultipartForm != nil {
				if cleanupErr := r.MultipartForm.RemoveAll(); cleanupErr != nil {
					log.Printf("error report temporary upload cleanup failed: %v", cleanupErr)
				}
			}
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "error_report_too_large", "Error report uploads cannot exceed 16 MiB")
				return submission, false
			}
			writeError(w, http.StatusBadRequest, "invalid_error_report", "The report or its screenshots exceed the upload limit or are malformed")
			return submission, false
		}
		if r.MultipartForm != nil {
			defer func() {
				if err := r.MultipartForm.RemoveAll(); err != nil {
					log.Printf("error report temporary upload cleanup failed: %v", err)
				}
			}()
			if files := r.MultipartForm.File["screenshots"]; len(files) > maxErrorScreenshots {
				writeError(w, http.StatusBadRequest, "too_many_screenshots", "Attach no more than 3 screenshots")
				return submission, false
			} else {
				for _, header := range files {
					attachment, err := readErrorReportAttachment(header)
					if err != nil {
						writeError(w, http.StatusBadRequest, "invalid_screenshot", err.Error())
						return submission, false
					}
					submission.attachments = append(submission.attachments, attachment)
				}
			}
		}
		submission.title = r.FormValue("title")
		submission.message = r.FormValue("message")
		submission.details = r.FormValue("details")
		submission.steps = r.FormValue("steps")
		submission.screen = r.FormValue("screen")
		metadata := r.FormValue("metadata")
		if len(metadata) > 8<<10 {
			writeError(w, http.StatusBadRequest, "invalid_error_report", "Diagnostic metadata is too large")
			return submission, false
		}
		if metadata != "" {
			if err := json.Unmarshal([]byte(metadata), &submission.metadata); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_error_report", "Diagnostic metadata must be valid JSON")
				return submission, false
			}
		}
		return submission, true
	}

	var input struct {
		Title    string         `json:"title"`
		Message  string         `json:"message"`
		Details  string         `json:"details"`
		Steps    string         `json:"steps"`
		Screen   string         `json:"screen"`
		Metadata map[string]any `json:"metadata"`
	}
	if !decodeJSON(w, r, &input) {
		return submission, false
	}
	submission.title = input.Title
	submission.message = input.Message
	submission.details = input.Details
	submission.steps = input.Steps
	submission.screen = input.Screen
	if input.Metadata != nil {
		submission.metadata = input.Metadata
	}
	return submission, true
}

func readErrorReportAttachment(header *multipart.FileHeader) (errorReportAttachment, error) {
	if header.Size <= 0 || header.Size > maxErrorScreenshot {
		return errorReportAttachment{}, errors.New("Each screenshot must be larger than 0 bytes and no larger than 5 MiB")
	}
	file, err := header.Open()
	if err != nil {
		return errorReportAttachment{}, errors.New("Could not read the uploaded screenshot")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxErrorScreenshot+1))
	if err != nil || len(data) == 0 || len(data) > maxErrorScreenshot {
		return errorReportAttachment{}, errors.New("Each screenshot must be no larger than 5 MiB")
	}
	contentType := http.DetectContentType(data)
	if contentType != "image/png" && contentType != "image/jpeg" && contentType != "image/webp" {
		return errorReportAttachment{}, errors.New("Screenshots must be PNG, JPEG, or WebP images")
	}
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(header.Filename), `\`, "/"))
	if name == "." || name == "" || len(name) > 255 {
		name = "screenshot"
	}
	return errorReportAttachment{
		id: newUUID(), filename: name, contentType: contentType, data: data,
	}, nil
}

func validateErrorReportSubmission(userID string, submission *errorReportSubmission) error {
	submission.title = strings.TrimSpace(submission.title)
	submission.message = strings.TrimSpace(submission.message)
	submission.details = strings.TrimSpace(submission.details)
	submission.steps = strings.TrimSpace(submission.steps)
	submission.screen = strings.TrimSpace(submission.screen)
	if submission.title == "" {
		titleRunes := []rune(submission.message)
		if len(titleRunes) > 120 {
			titleRunes = titleRunes[:120]
		}
		submission.title = string(titleRunes)
	}
	if _, err := newErrorReport(userID, submission.message, submission.screen, time.Now()); err != nil {
		return err
	}
	if len(submission.title) == 0 || len(submission.title) > 160 {
		return errors.New("A report title of up to 160 characters is required")
	}
	if len(submission.details) > 8000 || len(submission.steps) > 4000 {
		return errors.New("Additional details must be under 8000 characters and reproduction steps under 4000 characters")
	}
	if len(submission.metadata) > 24 {
		return errors.New("Diagnostic metadata contains too many fields")
	}
	encoded, err := json.Marshal(submission.metadata)
	if err != nil || len(encoded) > 8<<10 {
		return errors.New("Diagnostic metadata is too large")
	}
	return nil
}

func (a *API) adminErrorAttachment(w http.ResponseWriter, r *http.Request) {
	reportID, attachmentID := r.PathValue("id"), r.PathValue("attachmentId")
	if !validUUID(reportID) || !validUUID(attachmentID) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Error report and attachment ids must be UUIDs")
		return
	}
	var contentType, filename string
	var data []byte
	err := a.db.QueryRow(r.Context(), `
		SELECT content_type,filename,data FROM error_report_attachments
		WHERE report_id=$1 AND id=$2`, reportID, attachmentID).Scan(&contentType, &filename, &data)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Screenshot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load error screenshot")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(data); err != nil {
		log.Printf("admin error screenshot response write failed for attachment %s: %v", attachmentID, err)
	}
}
