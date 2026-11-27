package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type archiveFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
}

func (a *API) openLibraryPDF(w http.ResponseWriter, r *http.Request) {
	iaID := r.PathValue("iaId")
	if !archiveIdentifierPattern.MatchString(iaID) {
		writeError(w, http.StatusBadRequest, "invalid_archive_id", "Internet Archive identifier is invalid")
		return
	}

	client := *a.httpClient
	client.Timeout = 2 * time.Minute
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		host := strings.ToLower(request.URL.Hostname())
		if request.URL.Scheme != "https" || (host != "archive.org" && !strings.HasSuffix(host, ".archive.org")) {
			return errors.New("Archive.org redirected outside its domain")
		}
		if len(via) >= 10 {
			return errors.New("too many Archive.org redirects")
		}
		return nil
	}

	metadataURL := "https://archive.org/metadata/" + url.PathEscape(iaID) + "/files"
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, metadataURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not request Archive.org file metadata")
		return
	}
	metadataResponse, err := client.Do(request)
	if err != nil {
		if metadataResponse != nil && metadataResponse.Body != nil {
			_ = metadataResponse.Body.Close()
		}
		log.Printf("Archive.org metadata request failed for %q: %v", iaID, err)
		writeError(w, http.StatusBadGateway, "upstream_error", "Could not retrieve Archive.org file metadata")
		return
	}
	defer metadataResponse.Body.Close()
	if metadataResponse.StatusCode == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "public_pdf_not_found", "No public PDF is available for this Internet Archive item")
		return
	}
	if metadataResponse.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "upstream_error", "Could not retrieve Archive.org file metadata")
		return
	}

	metadata, err := io.ReadAll(io.LimitReader(metadataResponse.Body, (4<<20)+1))
	if err != nil || len(metadata) > 4<<20 {
		writeError(w, http.StatusBadGateway, "invalid_upstream_metadata", "Archive.org returned invalid file metadata")
		return
	}
	var filesResponse struct {
		Files []archiveFile `json:"result"`
	}
	if err := json.Unmarshal(metadata, &filesResponse); err != nil {
		writeError(w, http.StatusBadGateway, "invalid_upstream_metadata", "Archive.org returned invalid file metadata")
		return
	}
	pdfName := selectArchivePDF(iaID, filesResponse.Files)
	if pdfName == "" {
		writeError(w, http.StatusNotFound, "public_pdf_not_found", "No public PDF is available for this Internet Archive item")
		return
	}

	pdfURL := "https://archive.org/download/" + url.PathEscape(iaID) + "/" + url.PathEscape(pdfName)
	request, err = http.NewRequestWithContext(r.Context(), http.MethodGet, pdfURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not request the public-domain PDF")
		return
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		log.Printf("public-domain PDF request failed for %q: %v", iaID, err)
		writeError(w, http.StatusBadGateway, "upstream_error", "Could not retrieve the public-domain PDF")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "public_pdf_not_found", "The public PDF is no longer available")
		return
	}
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "upstream_error", "Could not retrieve the public-domain PDF")
		return
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, "application/pdf") {
		writeError(w, http.StatusBadGateway, "invalid_upstream_file", "Archive.org did not return a PDF")
		return
	}

	pdf := bufio.NewReader(response.Body)
	signature, err := pdf.Peek(5)
	if err != nil || string(signature) != "%PDF-" {
		writeError(w, http.StatusBadGateway, "invalid_upstream_file", "Archive.org did not return a valid PDF")
		return
	}

	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		log.Printf("could not extend public-domain PDF response deadline for %q: %v", iaID, err)
		writeError(w, http.StatusInternalServerError, "stream_error", "Could not stream the public-domain PDF")
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": pdfName,
	}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if response.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	if _, err := io.Copy(w, pdf); err != nil {
		log.Printf("public-domain PDF stream failed for %q: %v", iaID, err)
	}
}

func selectArchivePDF(iaID string, files []archiveFile) string {
	var exact, textPDF, otherPDF string
	for _, file := range files {
		name := file.Name
		lowerName := strings.ToLower(name)
		if !strings.HasSuffix(lowerName, ".pdf") ||
			name == "" || name == "." || name == ".." ||
			strings.ContainsAny(name, `/\`) {
			continue
		}
		switch {
		case strings.EqualFold(name, iaID+".pdf"):
			exact = name
		case strings.EqualFold(file.Format, "Text PDF") && textPDF == "":
			textPDF = name
		case otherPDF == "":
			otherPDF = name
		}
	}
	if exact != "" {
		return exact
	}
	if textPDF != "" {
		return textPDF
	}
	return otherPDF
}
