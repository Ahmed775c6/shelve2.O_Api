package api

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"
	"time"
)

func TestErrorReportScreenshotAcceptsOnlyBoundedImages(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("screenshots", "capture.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/error-reports", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	defer request.MultipartForm.RemoveAll()
	attachment, err := readErrorReportAttachment(request.MultipartForm.File["screenshots"][0])
	if err != nil {
		t.Fatalf("valid PNG rejected: %v", err)
	}
	if attachment.filename != "capture.png" || attachment.contentType != "image/png" || len(attachment.data) != len(png) {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}

	if _, err := readErrorReportAttachment(&multipart.FileHeader{Filename: "too-large.png", Size: maxErrorScreenshot + 1}); err == nil {
		t.Fatal("oversized screenshot was accepted")
	}
}

func TestErrorReportSubmissionRequiresBoundedDetails(t *testing.T) {
	submission := errorReportSubmission{
		title: "A problem", message: "The view failed", details: string(bytes.Repeat([]byte("x"), 8001)),
	}
	if err := validateErrorReportSubmission("user-id", &submission); err == nil {
		t.Fatal("oversized diagnostic details were accepted")
	}
	submission.details = "The view failed after loading"
	submission.steps = "Open the view and select a book"
	if err := validateErrorReportSubmission("user-id", &submission); err != nil {
		t.Fatalf("valid error report rejected: %v", err)
	}
	if submission.title == "" || submission.message == "" {
		t.Fatal("valid report fields were unexpectedly cleared")
	}
	if _, err := newErrorReport("", "error", "settings", time.Now()); err == nil {
		t.Fatal("unauthenticated error report accepted")
	}
}
