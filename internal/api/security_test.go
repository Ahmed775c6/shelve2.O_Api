package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWTExpirationAndSignature(t *testing.T) {
	if tokenLifetime != 24*time.Hour {
		t.Fatalf("token lifetime = %v, want 24 hours", tokenLifetime)
	}
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	secret := []byte(strings.Repeat("x", 32))
	token, err := signToken("123e4567-e89b-42d3-a456-426614174000", secret, now)
	if err != nil {
		t.Fatal(err)
	}

	got, err := verifyToken(token, secret, now.Add(tokenLifetime-time.Second))
	if err != nil || got != "123e4567-e89b-42d3-a456-426614174000" {
		t.Fatalf("valid token verification = (%q, %v)", got, err)
	}
	if _, err := verifyToken(token, secret, now.Add(tokenLifetime)); err == nil {
		t.Fatal("token should be expired at its exp time")
	}
	if _, err := verifyToken(token+"x", secret, now); err == nil {
		t.Fatal("tampered token should be rejected")
	}
}

func TestRememberedJWTExpiration(t *testing.T) {
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	secret := []byte(strings.Repeat("x", 32))
	token, err := signSessionToken("123e4567-e89b-42d3-a456-426614174000", secret, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyToken(token, secret, now.Add(rememberedTokenLifetime-time.Second)); err != nil {
		t.Fatalf("remembered token should still be valid before expiry: %v", err)
	}
	if _, err := verifyToken(token, secret, now.Add(rememberedTokenLifetime)); err == nil {
		t.Fatal("remembered token should be expired at its exp time")
	}
}

func TestAdminJWTCannotAuthenticateAsUser(t *testing.T) {
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	secret := []byte(strings.Repeat("a", 32))
	adminToken, err := signAdminToken("Ahmed", "admin", secret, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyAdminToken(adminToken, secret, now.Add(time.Hour))
	if err != nil || claims.Role != "admin" || claims.AdminName != "Ahmed" {
		t.Fatalf("admin token claims = (%+v, %v)", claims, err)
	}
	if _, err := verifyToken(adminToken, secret, now); err == nil {
		t.Fatal("admin token must not authenticate as a user")
	}
	userToken, err := signToken("123e4567-e89b-42d3-a456-426614174000", secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAdminToken(userToken, secret, now); err == nil {
		t.Fatal("user token must not authenticate as an admin")
	}
}

func TestAdminLoginAndUsageRoute(t *testing.T) {
	secret := []byte(strings.Repeat("b", 32))
	a := newAPI(nil, config{
		jwtSecret: secret, adminUsername: "Ahmed", adminPassword: "correct-admin-password",
	})
	loginBody, _ := json.Marshal(map[string]string{"username": "Ahmed", "password": "correct-admin-password"})
	login := httptest.NewRecorder()
	a.handler().ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewReader(loginBody)))
	if login.Code != http.StatusOK {
		t.Fatalf("admin login status = %d: %s", login.Code, login.Body.String())
	}
	var result struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || result.Role != "admin" {
		t.Fatalf("unexpected admin login response: %+v", result)
	}

	page := httptest.NewRecorder()
	a.handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/usage", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "SHELVE CONTROL CENTER") {
		t.Fatalf("/usage status = %d: %s", page.Code, page.Body.String())
	}
}

func TestAdminMutationRequiresPasswordReauthentication(t *testing.T) {
	secret := []byte(strings.Repeat("r", 32))
	token, err := signAdminToken("Ahmed", "admin", secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(nil, config{jwtSecret: secret, adminUsername: "Ahmed", adminPassword: "correct-admin-password"})
	handler := a.adminAuth(a.adminReauth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, password := range []string{"", "wrong-password", "correct-admin-password"} {
		request := httptest.NewRequest(http.MethodPost, "/api/admin/notifications", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Admin-Password", password)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if password == "correct-admin-password" {
			if response.Code == http.StatusUnauthorized {
				t.Fatalf("correct admin password rejected: %s", response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "admin_reauthentication_required") {
			t.Fatalf("password %q status = %d, want reauthentication required: %s", password, response.Code, response.Body.String())
		}
	}
}

func TestRequestSourceIPUsesConnectedPeer(t *testing.T) {
	tests := map[string]string{
		"192.0.2.1:8080":     "192.0.2.1",
		"[2001:db8::1]:8080": "2001:db8::1",
		"192.0.2.2":          "192.0.2.2",
		"not-an-ip:8080":     "",
	}
	for input, expected := range tests {
		if got := requestSourceIP(input); got != expected {
			t.Errorf("requestSourceIP(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestAuthMiddlewareUsesTokenSubjectAsOwner(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	expectedOwner := "123e4567-e89b-42d3-a456-426614174000"
	token, err := signToken(expectedOwner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(nil, config{jwtSecret: secret})
	handler := a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(userIDFrom(r)))
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != expectedOwner {
		t.Fatalf("auth middleware returned status %d and owner %q", response.Code, response.Body.String())
	}
}

func TestErrorReportUsesAuthenticatedUserAndBoundsPayload(t *testing.T) {
	userID := "123e4567-e89b-42d3-a456-426614174000"
	createdAt := time.Date(2026, 11, 19, 1, 0, 0, 0, time.FixedZone("local", 3600))
	report, err := newErrorReport(userID, "  Something failed  ", "reading-planner", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if report["userId"] != userID || report["message"] != "Something failed" || report["screen"] != "reading-planner" {
		t.Fatalf("unexpected report fields: %#v", report)
	}
	if report["createdAt"] != createdAt.UTC() {
		t.Fatalf("report timestamp = %v, want %v", report["createdAt"], createdAt.UTC())
	}

	if _, err := newErrorReport(userID, strings.Repeat("x", maxErrorReportMessage+1), "overview", createdAt); err == nil {
		t.Fatal("oversized error message should be rejected")
	}
	if _, err := newErrorReport(userID, "failure", "../private", createdAt); err == nil {
		t.Fatal("invalid screen identifier should be rejected")
	}
}

func TestErrorReportEndpointRequiresAuthentication(t *testing.T) {
	a := newAPI(nil, config{jwtSecret: []byte(strings.Repeat("s", 32))})
	request := httptest.NewRequest(http.MethodPost, "/api/error-reports", strings.NewReader(`{"message":"failure","userId":"forged"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestChangePasswordEndpointRequiresAuthentication(t *testing.T) {
	a := newAPI(nil, config{jwtSecret: []byte(strings.Repeat("s", 32))})
	request := httptest.NewRequest(http.MethodPatch, "/api/auth/password", strings.NewReader(`{"currentPassword":"old-password","newPassword":"new-password"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated password status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMergeTimestampPrecedence(t *testing.T) {
	base := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	if newerTimestampWins(base, base) {
		t.Fatal("equal timestamps must not replace an existing record")
	}
	if newerTimestampWins(base.Add(-time.Nanosecond), base) {
		t.Fatal("older record must not replace an existing record")
	}
	if !newerTimestampWins(base.Add(time.Nanosecond), base) {
		t.Fatal("newer record should replace an existing record")
	}
}

func TestUUIDAndUploadPathValidation(t *testing.T) {
	if !validUUID("123e4567-e89b-42d3-a456-426614174000") {
		t.Fatal("expected valid UUID to pass")
	}
	if validUUID("../../etc/passwd") {
		t.Fatal("path traversal string must not be a UUID")
	}
	if _, err := safeFilePath("uploads", "books", "../../escape.pdf"); err == nil {
		t.Fatal("traversal filename must be rejected")
	}
	if _, err := safeFilePath("uploads", "secrets", "123e4567-e89b-42d3-a456-426614174000.pdf"); err == nil {
		t.Fatal("unsupported file directory must be rejected")
	}
	if _, err := safeFilePath("uploads", "books", "123e4567-e89b-42d3-a456-426614174000.pdf"); err != nil {
		t.Fatalf("generated UUID filename should be accepted: %v", err)
	}
}
