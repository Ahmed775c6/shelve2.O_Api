package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type testEmail struct {
	to      string
	subject string
	body    string
}

type recordingEmailSender struct {
	messages []testEmail
	err      error
}

func (s *recordingEmailSender) Send(_ context.Context, to, subject, body string) error {
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, testEmail{to: to, subject: subject, body: body})
	return nil
}

func verificationTokenFromEmail(t *testing.T, message testEmail) string {
	t.Helper()
	for _, line := range strings.Split(message.body, "\n") {
		if !strings.HasPrefix(line, "https://") {
			continue
		}
		link, err := url.Parse(strings.TrimSpace(line))
		if err == nil && link.Query().Get("token") != "" {
			return link.Query().Get("token")
		}
	}
	t.Fatalf("verification email did not contain a valid link")
	return ""
}

func postgresTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("POSTGRES_TEST_URL"))
	if url == "" {
		t.Skip("set POSTGRES_TEST_URL to run PostgreSQL integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect PostgreSQL test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := MigratePostgres(context.Background(), pool); err != nil {
		t.Fatalf("migrate PostgreSQL test database: %v", err)
	}
	return pool
}

func postgresTestOwner(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := newUUID()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users(id,name,email,password_hash,created_at,updated_at)
		 VALUES($1,'Integration test',$2,'not-used',NOW(),NOW())`,
		id, id+"@example.test")
	if err != nil {
		t.Fatalf("insert integration test account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
	})
	return id
}

func testBookRecord(id, title string, updated time.Time) map[string]any {
	return map[string]any{
		"id": id, "title": title, "author": "Integration Author", "category": "Fiction",
		"coverImage": "", "coverColor": "#123456", "totalPages": 100, "currentPage": 0,
		"status": "unread", "uploadedAt": updated.Format(time.RFC3339Nano),
		"updatedAt": updated.Format(time.RFC3339Nano),
	}
}

func TestPostgresOfflineCloudPullRoundTrip(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	secret := []byte(strings.Repeat("i", 32))
	token, err := signToken(owner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret})
	bookID := newUUID()
	updated := time.Now().UTC().Truncate(time.Millisecond)
	record := testBookRecord(bookID, "Offline draft", updated)
	payload := map[string]any{
		"books": []any{record},
		"progress": []any{map[string]any{
			"id": bookID, "bookId": bookID, "currentPage": 12,
			"updatedAt": updated.Format(time.RFC3339Nano),
		}},
	}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/api/sync/merge", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("offline-to-cloud merge status = %d: %s", response.Code, response.Body.String())
	}

	pull := httptest.NewRequest(http.MethodGet, "/api/sync/pull", nil)
	pull.Header.Set("Authorization", "Bearer "+token)
	pulled := httptest.NewRecorder()
	a.handler().ServeHTTP(pulled, pull)
	if pulled.Code != http.StatusOK {
		t.Fatalf("cloud-to-offline pull status = %d: %s", pulled.Code, pulled.Body.String())
	}
	var result map[string][]map[string]any
	if err := json.Unmarshal(pulled.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result["books"]) != 1 || result["books"][0]["title"] != "Offline draft" {
		t.Fatalf("cloud pull did not contain the merged book: %#v", result["books"])
	}
	if len(result["progress"]) != 1 || result["progress"][0]["currentPage"] != float64(12) {
		t.Fatalf("cloud pull did not contain the related reading progress: %#v", result["progress"])
	}
}

func TestPostgresEqualTimestampSkipsWithoutConflict(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	a := newAPI(pool, config{})
	id := newUUID()
	updated := time.Now().UTC().Truncate(time.Microsecond)
	if outcome, err := a.mergeRecord(context.Background(), owner, "books", testBookRecord(id, "Cloud version", updated)); err != nil || outcome != "inserted" {
		t.Fatalf("initial merge = (%q, %v)", outcome, err)
	}
	if outcome, err := a.mergeRecord(context.Background(), owner, "books", testBookRecord(id, "Equal-time local version", updated)); err != nil || outcome != "skipped" {
		t.Fatalf("equal timestamp merge = (%q, %v)", outcome, err)
	}
	var conflicts int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_conflicts WHERE owner_id=$1 AND record_id=$2`, owner, id).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts != 0 {
		t.Fatalf("equal timestamps created %d conflicts", conflicts)
	}
}

func TestPostgresConcurrentEditCreatesConflict(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	a := newAPI(pool, config{})
	id := newUUID()
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	remote := testBookRecord(id, "Cloud edit", base.Add(20*time.Second))
	if _, err := a.mergeRecord(context.Background(), owner, "books", remote); err != nil {
		t.Fatal(err)
	}
	local := testBookRecord(id, "Offline edit", base)
	outcome, err := a.mergeRecord(context.Background(), owner, "books", local)
	if err != nil || outcome != "conflict" {
		t.Fatalf("concurrent merge = (%q, %v)", outcome, err)
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_conflicts WHERE owner_id=$1 AND collection='books' AND record_id=$2 AND resolved_at IS NULL`,
		owner, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unresolved conflict count = %d, want 1", count)
	}
}

func TestPostgresTombstonesAndDuplicateMerge(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	a := newAPI(pool, config{})
	id := newUUID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	book := testBookRecord(id, "To delete", now)
	if outcome, err := a.mergeRecord(context.Background(), owner, "books", book); err != nil || outcome != "inserted" {
		t.Fatalf("initial merge = (%q, %v)", outcome, err)
	}
	tombstone := testBookRecord(id, "To delete", now.Add(time.Second))
	tombstone["deletedAt"] = now.Add(time.Second).Format(time.RFC3339Nano)
	if outcome, err := a.mergeRecord(context.Background(), owner, "books", tombstone); err != nil || outcome != "updated" {
		t.Fatalf("tombstone merge = (%q, %v)", outcome, err)
	}
	if outcome, err := a.mergeRecord(context.Background(), owner, "books", tombstone); err != nil || outcome != "skipped" {
		t.Fatalf("duplicate merge = (%q, %v)", outcome, err)
	}
	var deleted bool
	if err := pool.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL FROM records WHERE owner_id=$1 AND collection='books' AND id=$2`, owner, id).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("duplicate merge cleared the book tombstone")
	}
}

func TestPostgresIdempotentUploadRetry(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	secret := []byte(strings.Repeat("u", 32))
	token, err := signToken(owner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret, uploadDir: t.TempDir()})
	uploadID := newUUID()
	for attempt := 0; attempt < 2; attempt++ {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("type", "books")
		_ = writer.WriteField("uploadId", uploadID)
		part, err := writer.CreateFormFile("file", "sample.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(part, "Shelve upload retry test.\n")
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("upload attempt %d status = %d: %s", attempt+1, response.Code, response.Body.String())
		}
		var result struct{ Filename string }
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Filename != uploadID+".txt" {
			t.Fatalf("idempotent filename = %q", result.Filename)
		}
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM files WHERE owner_id=$1 AND kind='books' AND upload_id=$2`, owner, uploadID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retry registered %d file rows, want 1", count)
	}
	data, err := os.ReadFile(filepath.Join(a.cfg.uploadDir, "books", uploadID+".txt"))
	if err != nil || string(data) != "Shelve upload retry test.\n" {
		t.Fatalf("idempotent upload content = %q, err=%v", data, err)
	}

	largeUploadID := newUUID()
	var largeBody bytes.Buffer
	largeWriter := multipart.NewWriter(&largeBody)
	_ = largeWriter.WriteField("type", "books")
	_ = largeWriter.WriteField("uploadId", largeUploadID)
	largePart, err := largeWriter.CreateFormFile("file", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(largePart, strings.Repeat("x", (20<<20)+1))
	if err := largeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	largeRequest := httptest.NewRequest(http.MethodPost, "/api/upload", &largeBody)
	largeRequest.Header.Set("Authorization", "Bearer "+token)
	largeRequest.Header.Set("Content-Type", largeWriter.FormDataContentType())
	largeResponse := httptest.NewRecorder()
	a.handler().ServeHTTP(largeResponse, largeRequest)
	if largeResponse.Code != http.StatusCreated {
		t.Fatalf("upload larger than 20 MiB status = %d: %s", largeResponse.Code, largeResponse.Body.String())
	}
}

func TestPostgresExpiredJWTLeavesCloudDataIntact(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	secret := []byte(strings.Repeat("e", 32))
	now := time.Now().UTC()
	token, err := signToken(owner, secret, now.Add(-tokenLifetime-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	record := testBookRecord(id, "Recover after sign-in", now)
	if _, err := newAPI(pool, config{}).mergeRecord(context.Background(), owner, "books", record); err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret})
	request := httptest.NewRequest(http.MethodGet, "/api/sync/pull", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	a.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM records WHERE owner_id=$1 AND id=$2`, owner, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("JWT rejection changed the synced data: count=%d", count)
	}
	recoveredToken, err := signToken(owner, secret, now)
	if err != nil {
		t.Fatal(err)
	}
	retry := httptest.NewRequest(http.MethodGet, "/api/sync/pull", nil)
	retry.Header.Set("Authorization", "Bearer "+recoveredToken)
	recovered := httptest.NewRecorder()
	a.handler().ServeHTTP(recovered, retry)
	if recovered.Code != http.StatusOK {
		t.Fatalf("sync after signing in again status = %d: %s", recovered.Code, recovered.Body.String())
	}
}

func TestPostgresSignupRequiresEmailVerification(t *testing.T) {
	pool := postgresTestPool(t)
	mailer := &recordingEmailSender{}
	secret := []byte(strings.Repeat("v", 32))
	a := newAPI(pool, config{
		jwtSecret: secret, mailer: mailer,
		verificationBaseURL: "https://shelve.example.test/verify-email",
	})
	email := newUUID() + "@example.test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE email=$1`, email)
	})
	body, _ := json.Marshal(map[string]string{
		"name": "Verification Test", "email": email, "password": "valid-password-123",
	})
	signup := httptest.NewRecorder()
	a.handler().ServeHTTP(signup, httptest.NewRequest(http.MethodPost, "/api/auth/signup", bytes.NewReader(body)))
	if signup.Code != http.StatusAccepted {
		t.Fatalf("signup status = %d: %s", signup.Code, signup.Body.String())
	}
	if len(mailer.messages) != 1 || mailer.messages[0].to != email {
		t.Fatalf("signup did not send one verification email: %#v", mailer.messages)
	}
	verificationToken := verificationTokenFromEmail(t, mailer.messages[0])

	signinBody, _ := json.Marshal(map[string]string{"email": email, "password": "valid-password-123"})
	signin := httptest.NewRecorder()
	a.handler().ServeHTTP(signin, httptest.NewRequest(http.MethodPost, "/api/auth/signin", bytes.NewReader(signinBody)))
	if signin.Code != http.StatusForbidden || !strings.Contains(signin.Body.String(), "email_not_verified") {
		t.Fatalf("unverified account sign-in status = %d: %s", signin.Code, signin.Body.String())
	}

	resendBody, _ := json.Marshal(map[string]string{"email": email})
	resend := httptest.NewRecorder()
	a.handler().ServeHTTP(resend, httptest.NewRequest(http.MethodPost, "/api/auth/verification/resend", bytes.NewReader(resendBody)))
	if resend.Code != http.StatusAccepted || len(mailer.messages) != 2 {
		t.Fatalf("verification resend status = %d, messages=%d: %s", resend.Code, len(mailer.messages), resend.Body.String())
	}
	oldTokenBody, _ := json.Marshal(map[string]string{"token": verificationToken})
	oldTokenResponse := httptest.NewRecorder()
	a.handler().ServeHTTP(oldTokenResponse, httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", bytes.NewReader(oldTokenBody)))
	if oldTokenResponse.Code != http.StatusBadRequest {
		t.Fatalf("superseded verification token status = %d, want %d", oldTokenResponse.Code, http.StatusBadRequest)
	}
	verificationToken = verificationTokenFromEmail(t, mailer.messages[1])
	if _, err := pool.Exec(context.Background(),
		`UPDATE email_verification_tokens SET expires_at=NOW()-INTERVAL '1 second'
		 WHERE user_id=(SELECT id FROM users WHERE email=$1)`, email); err != nil {
		t.Fatal(err)
	}
	verifyBody, _ := json.Marshal(map[string]string{"token": verificationToken})
	verify := httptest.NewRecorder()
	a.handler().ServeHTTP(verify, httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", bytes.NewReader(verifyBody)))
	if verify.Code != http.StatusBadRequest {
		t.Fatalf("expired verification token status = %d, want %d", verify.Code, http.StatusBadRequest)
	}
	resend = httptest.NewRecorder()
	a.handler().ServeHTTP(resend, httptest.NewRequest(http.MethodPost, "/api/auth/verification/resend", bytes.NewReader(resendBody)))
	if resend.Code != http.StatusAccepted || len(mailer.messages) != 3 {
		t.Fatalf("verification resend after expiry status = %d, messages=%d: %s", resend.Code, len(mailer.messages), resend.Body.String())
	}
	verifyBody, _ = json.Marshal(map[string]string{"token": verificationTokenFromEmail(t, mailer.messages[2])})
	verify = httptest.NewRecorder()
	a.handler().ServeHTTP(verify, httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", bytes.NewReader(verifyBody)))
	if verify.Code != http.StatusOK {
		t.Fatalf("email verification status = %d: %s", verify.Code, verify.Body.String())
	}
	signin = httptest.NewRecorder()
	a.handler().ServeHTTP(signin, httptest.NewRequest(http.MethodPost, "/api/auth/signin", bytes.NewReader(signinBody)))
	if signin.Code != http.StatusOK {
		t.Fatalf("verified account sign-in status = %d: %s", signin.Code, signin.Body.String())
	}
	reuse := httptest.NewRecorder()
	a.handler().ServeHTTP(reuse, httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", bytes.NewReader(verifyBody)))
	if reuse.Code != http.StatusBadRequest {
		t.Fatalf("reused verification token status = %d, want %d", reuse.Code, http.StatusBadRequest)
	}
}

func TestPostgresSignupAndSigninWithoutEmailConfigKeepAccountUnverified(t *testing.T) {
	pool := postgresTestPool(t)
	a := newAPI(pool, config{jwtSecret: []byte(strings.Repeat("u", 32))})
	email := newUUID() + "@example.test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE email=$1`, email)
	})
	body, _ := json.Marshal(map[string]string{
		"name": "Unverified Test", "email": email, "password": "valid-password-123",
	})
	signup := httptest.NewRecorder()
	a.handler().ServeHTTP(signup, httptest.NewRequest(http.MethodPost, "/api/auth/signup", bytes.NewReader(body)))
	if signup.Code != http.StatusCreated {
		t.Fatalf("signup status = %d: %s", signup.Code, signup.Body.String())
	}
	var signupPayload struct {
		VerificationRequired bool   `json:"verificationRequired"`
		EmailVerified        bool   `json:"emailVerified"`
		Email                string `json:"email"`
	}
	if err := json.Unmarshal(signup.Body.Bytes(), &signupPayload); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	if signupPayload.VerificationRequired || signupPayload.EmailVerified || signupPayload.Email != email {
		t.Fatalf("unexpected signup response: %+v", signupPayload)
	}

	var verified, hasVerificationToken bool
	if err := pool.QueryRow(context.Background(),
		`SELECT email_verified_at IS NOT NULL,
		        EXISTS(SELECT 1 FROM email_verification_tokens WHERE user_id=users.id)
		 FROM users WHERE email=$1`, email).Scan(&verified, &hasVerificationToken); err != nil {
		t.Fatal(err)
	}
	if verified || hasVerificationToken {
		t.Fatalf("unconfigured signup should remain unverified without a token: verified=%t token=%t",
			verified, hasVerificationToken)
	}

	signinBody, _ := json.Marshal(map[string]string{"email": email, "password": "valid-password-123"})
	signin := httptest.NewRecorder()
	a.handler().ServeHTTP(signin, httptest.NewRequest(http.MethodPost, "/api/auth/signin", bytes.NewReader(signinBody)))
	if signin.Code != http.StatusOK {
		t.Fatalf("unverified account sign-in status = %d: %s", signin.Code, signin.Body.String())
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT email_verified_at IS NOT NULL FROM users WHERE email=$1`, email).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if verified {
		t.Fatal("signing in without email configuration must not mark the account verified")
	}
}

func TestPostgresEmailNotificationOptInAndDedupe(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET email_verified_at=NOW() WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}

	secret := []byte(strings.Repeat("n", 32))
	token, err := signToken(owner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	mailer := &recordingEmailSender{}
	a := newAPI(pool, config{jwtSecret: secret, mailer: mailer})
	toggle := func(enabled bool) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]bool{"emailNotifications": enabled})
		request := httptest.NewRequest(http.MethodPatch, "/api/auth/email-notifications", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}
	if response := toggle(true); response.Code != http.StatusOK {
		t.Fatalf("enable email notifications status = %d: %s", response.Code, response.Body.String())
	}
	for range 2 {
		if err := a.sendOptInNotification(context.Background(), owner, "sync-conflict", "2026-11-21",
			"Shelve sync conflict", "Resolve a conflict in Shelve."); err != nil {
			t.Fatal(err)
		}
	}
	if len(mailer.messages) != 1 {
		t.Fatalf("duplicate event sent %d emails, want 1", len(mailer.messages))
	}
	if response := toggle(false); response.Code != http.StatusOK {
		t.Fatalf("disable email notifications status = %d: %s", response.Code, response.Body.String())
	}
	if err := a.sendOptInNotification(context.Background(), owner, "sync-conflict", "2026-11-22",
		"Shelve sync conflict", "Resolve a conflict in Shelve."); err != nil {
		t.Fatal(err)
	}
	if len(mailer.messages) != 1 {
		t.Fatalf("opted-out user received %d emails", len(mailer.messages))
	}
}

func TestPostgresPasswordResetTokenIsSingleUse(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET email_verified_at=NOW() WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	mailer := &recordingEmailSender{}
	a := newAPI(pool, config{mailer: mailer, verificationBaseURL: "https://shelve.example/verify-email"})
	var email string
	if err := pool.QueryRow(context.Background(), `SELECT email FROM users WHERE id=$1`, owner).Scan(&email); err != nil {
		t.Fatal(err)
	}
	requestBody, _ := json.Marshal(map[string]string{"email": email})
	request := httptest.NewRecorder()
	a.handler().ServeHTTP(request, httptest.NewRequest(http.MethodPost, "/api/auth/password/forgot", bytes.NewReader(requestBody)))
	if request.Code != http.StatusAccepted || len(mailer.messages) != 1 {
		t.Fatalf("password reset request status = %d, emails=%d: %s", request.Code, len(mailer.messages), request.Body.String())
	}

	token := verificationTokenFromEmail(t, mailer.messages[0])
	resetBody, _ := json.Marshal(map[string]string{"token": token, "newPassword": "new-valid-password"})
	reset := httptest.NewRecorder()
	a.handler().ServeHTTP(reset, httptest.NewRequest(http.MethodPost, "/api/auth/password/reset", bytes.NewReader(resetBody)))
	if reset.Code != http.StatusOK {
		t.Fatalf("password reset status = %d: %s", reset.Code, reset.Body.String())
	}
	var passwordHash string
	if err := pool.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE id=$1`, owner).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("new-valid-password")); err != nil {
		t.Fatalf("password was not updated: %v", err)
	}
	reuse := httptest.NewRecorder()
	a.handler().ServeHTTP(reuse, httptest.NewRequest(http.MethodPost, "/api/auth/password/reset", bytes.NewReader(resetBody)))
	if reuse.Code != http.StatusBadRequest {
		t.Fatalf("reused password reset token status = %d, want %d", reuse.Code, http.StatusBadRequest)
	}
}

func TestPostgresReplaceCloudDataRequiresPasswordAndReplacesRecords(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET password_hash=$1 WHERE id=$2`, string(passwordHash), owner); err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("r", 32))
	token, err := signToken(owner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret})
	oldID := newUUID()
	if _, err := a.mergeRecord(context.Background(), owner, "books", testBookRecord(oldID, "Old cloud book", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	newID := newUUID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	payload := map[string]any{
		"books":    []any{testBookRecord(newID, "Local source book", now)},
		"progress": []any{}, "notes": []any{}, "readingSessions": []any{},
		"readingPlans": []any{}, "writings": []any{}, "writingSessions": []any{},
	}
	call := func(password string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"password": password, "data": payload})
		request := httptest.NewRequest(http.MethodPost, "/api/sync/replace", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}
	if response := call("wrong-password"); response.Code != http.StatusUnauthorized {
		t.Fatalf("replacement with wrong password status = %d: %s", response.Code, response.Body.String())
	}
	var oldCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM records WHERE owner_id=$1 AND id=$2`, owner, oldID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 1 {
		t.Fatalf("wrong password changed existing cloud data: old record count = %d", oldCount)
	}
	if response := call("correct-password"); response.Code != http.StatusOK {
		t.Fatalf("replacement status = %d: %s", response.Code, response.Body.String())
	}
	var newCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM records WHERE owner_id=$1 AND id=$2`, owner, oldID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM records WHERE owner_id=$1 AND id=$2`, owner, newID).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || newCount != 1 {
		t.Fatalf("replacement records: old=%d, new=%d", oldCount, newCount)
	}
}

func TestPostgresPasswordVerification(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET password_hash=$1 WHERE id=$2`, string(passwordHash), owner); err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("v", 32))
	token, err := signToken(owner, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret})
	verify := func(password string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"password": password})
		request := httptest.NewRequest(http.MethodPost, "/api/auth/password/verify", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}
	if response := verify("wrong-password"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if response := verify("correct-password"); response.Code != http.StatusOK {
		t.Fatalf("correct password status = %d: %s", response.Code, response.Body.String())
	}
}

func TestPostgresSignInSessionPreferenceAndProfileUpdate(t *testing.T) {
	pool := postgresTestPool(t)
	owner := postgresTestOwner(t, pool)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET password_hash=$1 WHERE id=$2`, string(passwordHash), owner); err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("p", 32))
	a := newAPI(pool, config{jwtSecret: secret})
	var email string
	if err := pool.QueryRow(context.Background(), `SELECT email FROM users WHERE id=$1`, owner).Scan(&email); err != nil {
		t.Fatal(err)
	}
	signinBody, _ := json.Marshal(map[string]any{
		"email": email, "password": "correct-password", "rememberMe": true,
	})
	signin := httptest.NewRecorder()
	a.handler().ServeHTTP(signin, httptest.NewRequest(http.MethodPost, "/api/auth/signin", bytes.NewReader(signinBody)))
	if signin.Code != http.StatusOK {
		t.Fatalf("remembered sign-in status = %d: %s", signin.Code, signin.Body.String())
	}
	var rememberedSession struct {
		Token      string    `json:"token"`
		ExpiresAt  time.Time `json:"expiresAt"`
		RememberMe bool      `json:"rememberMe"`
	}
	if err := json.Unmarshal(signin.Body.Bytes(), &rememberedSession); err != nil {
		t.Fatal(err)
	}
	if !rememberedSession.RememberMe || time.Until(rememberedSession.ExpiresAt) < rememberedTokenLifetime-time.Minute {
		t.Fatalf("remembered session has an unexpected expiry: %+v", rememberedSession)
	}
	if _, err := verifyToken(rememberedSession.Token, secret, time.Now().UTC().Add(25*time.Hour)); err != nil {
		t.Fatalf("remembered token did not last beyond 24 hours: %v", err)
	}

	updateBody, _ := json.Marshal(map[string]any{"password": "correct-password", "rememberMe": false})
	update := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPatch, "/api/auth/session", bytes.NewReader(updateBody))
	updateRequest.Header.Set("Authorization", "Bearer "+rememberedSession.Token)
	a.handler().ServeHTTP(update, updateRequest)
	if update.Code != http.StatusOK {
		t.Fatalf("session preference update status = %d: %s", update.Code, update.Body.String())
	}
	var regularSession struct {
		Token      string    `json:"token"`
		ExpiresAt  time.Time `json:"expiresAt"`
		RememberMe bool      `json:"rememberMe"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &regularSession); err != nil {
		t.Fatal(err)
	}
	if regularSession.RememberMe || time.Until(regularSession.ExpiresAt) < tokenLifetime-time.Minute {
		t.Fatalf("regular session has an unexpected expiry: %+v", regularSession)
	}
	if _, err := verifyToken(regularSession.Token, secret, time.Now().UTC().Add(25*time.Hour)); err == nil {
		t.Fatal("regular token should expire before 25 hours")
	}

	profileBody, _ := json.Marshal(map[string]string{"name": "Updated Name", "password": "correct-password"})
	profile := httptest.NewRecorder()
	profileRequest := httptest.NewRequest(http.MethodPatch, "/api/auth/profile", bytes.NewReader(profileBody))
	profileRequest.Header.Set("Authorization", "Bearer "+regularSession.Token)
	a.handler().ServeHTTP(profile, profileRequest)
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), `"name":"Updated Name"`) {
		t.Fatalf("profile update status = %d: %s", profile.Code, profile.Body.String())
	}
}
