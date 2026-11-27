package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresAdminPublicBooksNotificationsAndRequestHistory(t *testing.T) {
	pool := postgresTestPool(t)
	userID := postgresTestOwner(t, pool)
	secret := []byte(strings.Repeat("d", 32))
	adminToken, err := signAdminToken("Ahmed", "admin", secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	userToken, err := signToken(userID, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{
		jwtSecret: secret, adminUsername: "Ahmed", adminPassword: "integration-admin-password",
	})
	adminCall := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var requestBody []byte
		if body != nil {
			requestBody, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+adminToken)
		if method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete {
			request.Header.Set("X-Admin-Password", "integration-admin-password")
		}
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}
	userCall := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var requestBody []byte
		if body != nil {
			requestBody, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+userToken)
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}

	published := adminCall(http.MethodPost, "/api/admin/books", map[string]any{
		"title": "Admin Published Book", "author": "Shelve Press", "category": "Fiction",
		"description": "A community edition.", "coverColor": "cover-blue", "totalPages": 180,
	})
	if published.Code != http.StatusCreated {
		t.Fatalf("publish book status = %d: %s", published.Code, published.Body.String())
	}
	var book struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(published.Body.Bytes(), &book); err != nil || !validUUID(book.ID) {
		t.Fatalf("published book id = %q, decode error = %v", book.ID, err)
	}
	publicList := httptest.NewRecorder()
	a.handler().ServeHTTP(publicList, httptest.NewRequest(http.MethodGet, "/api/public/books", nil))
	if publicList.Code != http.StatusOK || !strings.Contains(publicList.Body.String(), "Admin Published Book") {
		t.Fatalf("public book list status = %d: %s", publicList.Code, publicList.Body.String())
	}
	added := userCall(http.MethodPost, "/api/public/books/"+book.ID+"/add", nil)
	if added.Code != http.StatusCreated {
		t.Fatalf("add public book status = %d: %s", added.Code, added.Body.String())
	}
	var addedBook map[string]any
	if err := json.Unmarshal(added.Body.Bytes(), &addedBook); err != nil {
		t.Fatal(err)
	}
	if addedBook["title"] != "Admin Published Book" || addedBook["status"] != "unread" {
		t.Fatalf("unexpected added book: %#v", addedBook)
	}

	notification := adminCall(http.MethodPost, "/api/admin/notifications", map[string]any{
		"userIds": []string{userID}, "title": "Welcome", "body": "A Shelve update.", "channel": "in_app",
	})
	if notification.Code != http.StatusCreated {
		t.Fatalf("admin notification status = %d: %s", notification.Code, notification.Body.String())
	}
	notifications := userCall(http.MethodGet, "/api/notifications", nil)
	if notifications.Code != http.StatusOK || !strings.Contains(notifications.Body.String(), "A Shelve update.") {
		t.Fatalf("user notifications status = %d: %s", notifications.Code, notifications.Body.String())
	}

	report := userCall(http.MethodPost, "/api/error-reports", map[string]string{
		"message": "Integration-test report", "screen": "settings",
	})
	if report.Code != http.StatusCreated {
		t.Fatalf("error report status = %d: %s", report.Code, report.Body.String())
	}
	errorsResponse := adminCall(http.MethodGet, "/api/admin/errors", nil)
	if errorsResponse.Code != http.StatusOK || !strings.Contains(errorsResponse.Body.String(), "Integration-test report") {
		t.Fatalf("admin errors status = %d: %s", errorsResponse.Code, errorsResponse.Body.String())
	}
	feedback := userCall(http.MethodPost, "/api/reports", map[string]string{
		"category": "idea", "message": "Integration-test product idea",
	})
	if feedback.Code != http.StatusCreated {
		t.Fatalf("user insight status = %d: %s", feedback.Code, feedback.Body.String())
	}
	insights := adminCall(http.MethodGet, "/api/admin/reports", nil)
	if insights.Code != http.StatusOK || !strings.Contains(insights.Body.String(), "Integration-test product idea") {
		t.Fatalf("admin insights status = %d: %s", insights.Code, insights.Body.String())
	}
	var insightItems []map[string]any
	if err := json.Unmarshal(insights.Body.Bytes(), &insightItems); err != nil || len(insightItems) == 0 {
		t.Fatalf("could not decode admin insights: %v", err)
	}
	insightID, _ := insightItems[0]["id"].(string)
	if !validUUID(insightID) {
		t.Fatalf("insight id = %q", insightID)
	}
	updatedInsight := adminCall(http.MethodPatch, "/api/admin/reports/"+insightID, map[string]string{"status": "reviewing"})
	if updatedInsight.Code != http.StatusOK {
		t.Fatalf("update insight status = %d: %s", updatedInsight.Code, updatedInsight.Body.String())
	}
	userDetails := adminCall(http.MethodGet, "/api/admin/users/"+userID, nil)
	if userDetails.Code != http.StatusOK || !strings.Contains(userDetails.Body.String(), "Admin Published Book") {
		t.Fatalf("admin user details status = %d: %s", userDetails.Code, userDetails.Body.String())
	}
	updatedUser := adminCall(http.MethodPatch, "/api/admin/users/"+userID, map[string]any{
		"name": "Integration User", "email": userID + "@example.test", "banned": true, "password": "new-integration-password",
	})
	if updatedUser.Code != http.StatusOK {
		t.Fatalf("admin user control status = %d: %s", updatedUser.Code, updatedUser.Body.String())
	}
	blockedUser := userCall(http.MethodGet, "/api/notifications", nil)
	if blockedUser.Code != http.StatusForbidden {
		t.Fatalf("banned account status = %d, want forbidden: %s", blockedUser.Code, blockedUser.Body.String())
	}
	unbannedUser := adminCall(http.MethodPatch, "/api/admin/users/"+userID, map[string]any{
		"name": "Integration User", "email": userID + "@example.test", "banned": false,
	})
	if unbannedUser.Code != http.StatusOK {
		t.Fatalf("unban user status = %d: %s", unbannedUser.Code, unbannedUser.Body.String())
	}
	revokedUser := userCall(http.MethodGet, "/api/notifications", nil)
	if revokedUser.Code != http.StatusUnauthorized {
		t.Fatalf("password-reset session status = %d, want unauthorized: %s", revokedUser.Code, revokedUser.Body.String())
	}
	requests := adminCall(http.MethodGet, "/api/admin/requests?limit=100", nil)
	if requests.Code != http.StatusOK || !strings.Contains(requests.Body.String(), "/api/admin/books") {
		t.Fatalf("request history status = %d: %s", requests.Code, requests.Body.String())
	}
	var requestItems []map[string]any
	if err := json.Unmarshal(requests.Body.Bytes(), &requestItems); err != nil {
		t.Fatal(err)
	}
	var attributed bool
	for _, item := range requestItems {
		if item["path"] == "/api/admin/books" {
			attributed = item["actorType"] == "admin" && item["ipAddress"] == "192.0.2.1"
			break
		}
	}
	if !attributed {
		t.Fatalf("admin request missing actor or peer IP attribution: %#v", requestItems)
	}

	var stored int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM records WHERE owner_id=$1 AND collection='books' AND data->>'title'=$2`,
		userID, "Admin Published Book").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("user library has %d published-book copies, want 1", stored)
	}
}

func TestPostgresAdminAnalyticsHandlesFractionalAverageDuration(t *testing.T) {
	pool := postgresTestPool(t)
	ctx := context.Background()
	usageDay := time.Now().UTC().Format("2006-01-02")
	var existingRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM request_daily_usage WHERE usage_day=$1::date`, usageDay).Scan(&existingRows); err != nil {
		t.Fatal(err)
	}

	var requestCount, successCount, redirectCount, clientErrors, serverErrors int64
	var newUsers, totalDuration, maxDuration int64
	if existingRows > 0 {
		if err := pool.QueryRow(ctx, `
			SELECT request_count,success_count,redirect_count,client_error_count,server_error_count,
			       new_user_count,total_duration_ms,max_duration_ms
			FROM request_daily_usage WHERE usage_day=$1::date`, usageDay).
			Scan(&requestCount, &successCount, &redirectCount, &clientErrors, &serverErrors,
				&newUsers, &totalDuration, &maxDuration); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if existingRows == 0 {
			if _, err := pool.Exec(ctx, `DELETE FROM request_daily_usage WHERE usage_day=$1::date`, usageDay); err != nil {
				t.Errorf("remove test usage aggregate: %v", err)
			}
			return
		}
		if _, err := pool.Exec(ctx, `
			UPDATE request_daily_usage SET request_count=$2,success_count=$3,redirect_count=$4,
			       client_error_count=$5,server_error_count=$6,new_user_count=$7,
			       total_duration_ms=$8,max_duration_ms=$9 WHERE usage_day=$1::date`,
			usageDay, requestCount, successCount, redirectCount, clientErrors, serverErrors,
			newUsers, totalDuration, maxDuration); err != nil {
			t.Errorf("restore usage aggregate: %v", err)
		}
	})

	extraRequests := totalDuration + 2
	if _, err := pool.Exec(ctx, `
		INSERT INTO request_daily_usage(
			usage_day,request_count,success_count,redirect_count,client_error_count,
			server_error_count,new_user_count,total_duration_ms,max_duration_ms
		) VALUES($1::date,$2,$2,0,0,0,0,$3,1)
		ON CONFLICT(usage_day) DO UPDATE SET
			request_count=EXCLUDED.request_count,success_count=EXCLUDED.success_count,
			redirect_count=EXCLUDED.redirect_count,client_error_count=EXCLUDED.client_error_count,
			server_error_count=EXCLUDED.server_error_count,new_user_count=EXCLUDED.new_user_count,
			total_duration_ms=EXCLUDED.total_duration_ms,max_duration_ms=EXCLUDED.max_duration_ms`,
		usageDay, requestCount+extraRequests, totalDuration+1); err != nil {
		t.Fatal(err)
	}

	a := newAPI(pool, config{jwtSecret: []byte(strings.Repeat("a", 32))})
	response := httptest.NewRecorder()
	a.adminAnalytics(response, httptest.NewRequest(http.MethodGet, "/api/admin/analytics?period=day", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("analytics status = %d: %s", response.Code, response.Body.String())
	}
}

func TestPostgresAdminNotesAndUserNotificationDeletion(t *testing.T) {
	pool := postgresTestPool(t)
	userID := postgresTestOwner(t, pool)
	secret := []byte(strings.Repeat("n", 32))
	adminToken, err := signAdminToken("NotesAdmin", "admin", secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	userToken, err := signToken(userID, secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(pool, config{jwtSecret: secret, adminUsername: "NotesAdmin", adminPassword: "integration-admin-password"})
	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var requestBody []byte
		if body != nil {
			requestBody, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+token)
		if method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete {
			request.Header.Set("X-Admin-Password", "integration-admin-password")
		}
		response := httptest.NewRecorder()
		a.handler().ServeHTTP(response, request)
		return response
	}

	noteResponse := call(http.MethodPost, "/api/admin/notes", adminToken, adminNoteInput{
		Title: "Integration note", Content: "Private admin-only reminder.",
	})
	if noteResponse.Code != http.StatusCreated {
		t.Fatalf("create admin note status = %d: %s", noteResponse.Code, noteResponse.Body.String())
	}
	var note struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(noteResponse.Body.Bytes(), &note); err != nil || !validUUID(note.ID) || note.Content != "Private admin-only reminder." {
		t.Fatalf("invalid admin note response: %#v (decode error %v)", note, err)
	}
	updatedNote := call(http.MethodPatch, "/api/admin/notes/"+note.ID, adminToken, adminNoteInput{
		Title: "Updated note", Content: "Updated private reminder.",
	})
	if updatedNote.Code != http.StatusOK {
		t.Fatalf("update admin note status = %d: %s", updatedNote.Code, updatedNote.Body.String())
	}
	noteList := call(http.MethodGet, "/api/admin/notes", adminToken, nil)
	if noteList.Code != http.StatusOK || !strings.Contains(noteList.Body.String(), "Updated private reminder.") {
		t.Fatalf("list admin notes status = %d: %s", noteList.Code, noteList.Body.String())
	}
	deletedNote := call(http.MethodDelete, "/api/admin/notes/"+note.ID, adminToken, nil)
	if deletedNote.Code != http.StatusOK {
		t.Fatalf("delete admin note status = %d: %s", deletedNote.Code, deletedNote.Body.String())
	}

	notificationID := newUUID()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO user_notifications(id,user_id,title,body) VALUES($1,$2,'Delete test','Notification body')`,
		notificationID, userID); err != nil {
		t.Fatal(err)
	}
	deletedNotification := call(http.MethodDelete, "/api/notifications/"+notificationID, userToken, nil)
	if deletedNotification.Code != http.StatusOK {
		t.Fatalf("delete user notification status = %d: %s", deletedNotification.Code, deletedNotification.Body.String())
	}
	bulkNotificationIDs := []string{newUUID(), newUUID()}
	for _, id := range bulkNotificationIDs {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO user_notifications(id,user_id,title,body) VALUES($1,$2,'Bulk test','Notification body')`,
			id, userID); err != nil {
			t.Fatal(err)
		}
	}
	deletedAll := call(http.MethodDelete, "/api/notifications", userToken, nil)
	if deletedAll.Code != http.StatusOK || !strings.Contains(deletedAll.Body.String(), `"deleted":2`) {
		t.Fatalf("delete all notifications status = %d: %s", deletedAll.Code, deletedAll.Body.String())
	}
}
