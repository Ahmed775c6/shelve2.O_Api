package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	writeDeadline time.Time
}

func (recorder *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.writeDeadline = deadline
	return nil
}

func TestOpenLibraryPDFStreamsPDFAndFollowsArchiveRedirect(t *testing.T) {
	requestCount := 0
	api := newAPI(nil, config{})
	api.httpClient.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount == 1 {
			if request.URL.Host != "archive.org" || request.URL.Path != "/metadata/ia.book-1/files" {
				t.Fatalf("Archive.org metadata URL = %q", request.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"result":[{"name":"ia.book-1_text.pdf","format":"Text PDF"}]}`)),
				Request:    request,
			}, nil
		}
		if requestCount == 2 {
			if request.URL.Host != "archive.org" || request.URL.Path != "/download/ia.book-1/ia.book-1_text.pdf" {
				t.Fatalf("Archive.org PDF URL = %q", request.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://ia801.archive.org/download/ia.book-1/ia.book-1_text.pdf"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}
		if request.URL.Host != "ia801.archive.org" {
			t.Fatalf("redirect URL = %q", request.URL.String())
		}
		body := "%PDF-1.7\npublic-domain book"
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/pdf"}},
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       request,
		}, nil
	})

	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/openlibrary-pdf/ia.book-1", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("PDF proxy status = %d: %s", response.Code, response.Body.String())
	}
	if requestCount != 3 {
		t.Fatalf("Archive.org request count = %d, want 3", requestCount)
	}
	if response.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Content-Disposition") != "attachment; filename=ia.book-1_text.pdf" {
		t.Fatalf("Content-Disposition = %q", response.Header().Get("Content-Disposition"))
	}
	if !strings.HasPrefix(response.Body.String(), "%PDF-") {
		t.Fatalf("response does not contain PDF data: %q", response.Body.String())
	}
	if response.writeDeadline.Before(time.Now()) {
		t.Fatalf("PDF stream write deadline was not extended: %v", response.writeDeadline)
	}
}

func TestOpenLibraryPDFRejectsInvalidIdentifierAndNonPDF(t *testing.T) {
	t.Run("invalid identifier", func(t *testing.T) {
		api := newAPI(nil, config{})
		api.httpClient.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid identifier must not make an upstream request")
			return nil, nil
		})
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/openlibrary-pdf/invalid!id", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid identifier status = %d", response.Code)
		}
	})

	t.Run("no public PDF", func(t *testing.T) {
		api := newAPI(nil, config{})
		api.httpClient.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"result":[{"name":"book.epub","format":"EPUB"}]}`)),
				Request:    request,
			}, nil
		})
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/openlibrary-pdf/ia_book-1", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("item without PDF status = %d: %s", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "No public PDF is available") {
			t.Fatalf("item without PDF error = %s", response.Body.String())
		}
	})

	t.Run("non-PDF response", func(t *testing.T) {
		api := newAPI(nil, config{})
		requestCount := 0
		api.httpClient.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requestCount++
			if requestCount == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"result":[{"name":"ia_book-1.pdf","format":"Text PDF"}]}`)),
					Request:    request,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader("<html>not a PDF</html>")),
				Request:    request,
			}, nil
		})
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/openlibrary-pdf/ia_book-1", nil))
		if response.Code != http.StatusBadGateway {
			t.Fatalf("non-PDF upstream status = %d", response.Code)
		}
	})
}

func TestSelectArchivePDFPrefersExactFileAndRejectsPaths(t *testing.T) {
	files := []archiveFile{
		{Name: "../unsafe.pdf", Format: "Text PDF"},
		{Name: "other_text.pdf", Format: "Text PDF"},
		{Name: "ia.book-1.pdf", Format: "PDF"},
	}
	if got := selectArchivePDF("ia.book-1", files); got != "ia.book-1.pdf" {
		t.Fatalf("selected PDF = %q, want exact filename", got)
	}
	if got := selectArchivePDF("missing", files); got != "other_text.pdf" {
		t.Fatalf("selected fallback PDF = %q, want text PDF", got)
	}
}
