package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type statusRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(body)
	w.bytesWritten += int64(written)
	return written, err
}

func (a *API) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if a.db == nil {
			return
		}
		actorType, actorID := "anonymous", ""
		if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
			if claims, err := verifyAdminToken(token, a.cfg.jwtSecret, time.Now().UTC()); err == nil {
				actorType, actorID = "admin", claims.AdminName
			} else if userID, err := verifyToken(token, a.cfg.jwtSecret, time.Now().UTC()); err == nil {
				actorType, actorID = "user", userID
			}
		}
		sourceIP := requestSourceIP(r.RemoteAddr)
		requestBytes := r.ContentLength
		if requestBytes < 0 {
			requestBytes = 0
		}
		duration := time.Since(started).Milliseconds()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := a.db.Exec(ctx,
			`INSERT INTO request_logs(method,path,status,duration_ms,created_at,actor_type,actor_id,source_ip,user_agent,request_bytes,response_bytes)
			 VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::inet,$9,$10,$11)`,
			r.Method, r.URL.Path, status, duration, started.UTC(), actorType, actorID,
			sourceIP, requestUserAgent(r), requestBytes, recorder.bytesWritten); err != nil {
			log.Printf("request telemetry write failed: %v", err)
			return
		}
		success, redirects, clientErrors, serverErrors := int64(0), int64(0), int64(0), int64(0)
		switch {
		case status >= 200 && status < 300:
			success = 1
		case status >= 300 && status < 400:
			redirects = 1
		case status >= 400 && status < 500:
			clientErrors = 1
		case status >= 500:
			serverErrors = 1
		}
		newUsers := int64(0)
		if r.URL.Path == "/api/auth/signup" && status == http.StatusCreated {
			newUsers = 1
		}
		if _, err := a.db.Exec(ctx, `
			INSERT INTO request_daily_usage(
				usage_day,request_count,success_count,redirect_count,client_error_count,server_error_count,new_user_count,total_duration_ms,max_duration_ms
			)
			VALUES($1,1,$2,$3,$4,$5,$6,$7,$7)
			ON CONFLICT(usage_day) DO UPDATE SET
				request_count=request_daily_usage.request_count+1,
				success_count=request_daily_usage.success_count+EXCLUDED.success_count,
				redirect_count=request_daily_usage.redirect_count+EXCLUDED.redirect_count,
				client_error_count=request_daily_usage.client_error_count+EXCLUDED.client_error_count,
				server_error_count=request_daily_usage.server_error_count+EXCLUDED.server_error_count,
				new_user_count=request_daily_usage.new_user_count+EXCLUDED.new_user_count,
				total_duration_ms=request_daily_usage.total_duration_ms+EXCLUDED.total_duration_ms,
				max_duration_ms=GREATEST(request_daily_usage.max_duration_ms,EXCLUDED.max_duration_ms)`,
			started.UTC(), success, redirects, clientErrors, serverErrors, newUsers, duration); err != nil {
			log.Printf("daily request telemetry write failed: %v", err)
		}
		sequence := atomic.AddUint64(&a.requestSeq, 1)
		if sequence == 1 || sequence%100 == 0 {
			if err := a.recordServerDailySnapshot(ctx, started); err != nil {
				log.Printf("daily server telemetry write failed: %v", err)
			}
			if _, err := a.db.Exec(ctx, `DELETE FROM request_logs WHERE created_at<NOW()-INTERVAL '30 days'`); err != nil {
				log.Printf("request telemetry retention cleanup failed: %v", err)
			}
		}
	})
}

func requestSourceIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.String()
	}
	return ""
}

func runtimePlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func requestUserAgent(r *http.Request) string {
	userAgent := r.UserAgent()
	if len(userAgent) > 512 {
		return userAgent[:512]
	}
	return userAgent
}

func (a *API) recordServerDailySnapshot(ctx context.Context, observedAt time.Time) error {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	stats := a.db.Stat()
	var databaseSize int64
	if err := a.db.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&databaseSize); err != nil {
		return err
	}
	_, err := a.db.Exec(ctx, `
		INSERT INTO admin_server_daily(
			usage_day,peak_memory_bytes,peak_goroutines,peak_database_connections,peak_database_size_bytes,peak_uptime_seconds,last_observed_at
		) VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(usage_day) DO UPDATE SET
			peak_memory_bytes=GREATEST(admin_server_daily.peak_memory_bytes,EXCLUDED.peak_memory_bytes),
			peak_goroutines=GREATEST(admin_server_daily.peak_goroutines,EXCLUDED.peak_goroutines),
			peak_database_connections=GREATEST(admin_server_daily.peak_database_connections,EXCLUDED.peak_database_connections),
			peak_database_size_bytes=GREATEST(admin_server_daily.peak_database_size_bytes,EXCLUDED.peak_database_size_bytes),
			peak_uptime_seconds=GREATEST(admin_server_daily.peak_uptime_seconds,EXCLUDED.peak_uptime_seconds),
			last_observed_at=EXCLUDED.last_observed_at`,
		observedAt.UTC(), int64(memory.Alloc), int64(runtime.NumGoroutine()),
		int64(stats.TotalConns()), databaseSize, int64(time.Since(a.startedAt).Seconds()), observedAt.UTC())
	return err
}

func (a *API) serveUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/usage/")
	if name == r.URL.Path || name == "" {
		name = "index.html"
	}
	name = strings.TrimPrefix(name, "/")
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		http.NotFound(w, r)
		return
	}
	content, err := usageFiles.ReadFile("usage_next/out/" + name)
	if errors.Is(err, fs.ErrNotExist) {
		if !strings.Contains(name, ".") || strings.HasSuffix(name, ".html") {
			content, err = usageFiles.ReadFile("usage_next/out/index.html")
			if err == nil {
				name = "index.html"
			}
		}
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
	}
	if err != nil {
		log.Printf("admin dashboard asset read failed: %v", err)
		http.Error(w, "Dashboard unavailable", http.StatusInternalServerError)
		return
	}
	contentType := mime.TypeByExtension("." + strings.TrimPrefix(name[strings.LastIndex(name, ".")+1:], "."))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; connect-src 'self'")
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}

func (a *API) adminLogin(w http.ResponseWriter, r *http.Request) {
	rateRequest := r.Clone(r.Context())
	rateRequest.Header.Del("X-Forwarded-For")
	if !a.allowRate(rateRequest, 8, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many admin login attempts")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	username := strings.TrimSpace(input.Username)
	role := ""
	if a.cfg.developerUsername != "" && secureStringEqual(username, a.cfg.developerUsername) &&
		a.cfg.developerPassword != "" && secureStringEqual(input.Password, a.cfg.developerPassword) {
		role = "developer"
	} else {
		authenticated, err := a.authenticatePrimaryAdmin(r.Context(), username, input.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not verify admin account")
			return
		}
		if authenticated {
			role = "admin"
		}
	}
	if role == "" {
		writeError(w, http.StatusUnauthorized, "invalid_admin_credentials", "Username or password is incorrect")
		return
	}
	now := time.Now().UTC()
	token, err := signAdminToken(username, role, a.cfg.jwtSecret, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not create admin session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "expiresAt": now.Add(adminTokenLifetime), "username": username, "role": role,
	})
}

func (a *API) adminMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": adminNameFrom(r), "role": adminRoleFrom(r)})
}

func (a *API) adminDashboard(w http.ResponseWriter, r *http.Request) {
	var users, books, publicBooks, unresolvedErrors, requests24h, databaseSizeBytes int64
	ctx := r.Context()
	if err := a.recordServerDailySnapshot(ctx, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not record server health")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard statistics")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM records WHERE collection='books' AND deleted_at IS NULL`).Scan(&books); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard statistics")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM shelf_public_books`).Scan(&publicBooks); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard statistics")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM error_reports WHERE resolved_at IS NULL`).Scan(&unresolvedErrors); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard statistics")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM request_logs WHERE created_at>NOW()-INTERVAL '24 hours'`).Scan(&requests24h); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard statistics")
		return
	}
	if err := a.db.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&databaseSizeBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load database size")
		return
	}
	stats := a.db.Stat()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	userTrend, err := a.adminDailyCounts(ctx, `SELECT TO_CHAR(series.day,'YYYY-MM-DD'),COUNT(u.id)
		FROM generate_series(CURRENT_DATE-INTERVAL '6 days',CURRENT_DATE,INTERVAL '1 day') AS series(day)
		LEFT JOIN users u ON u.created_at::date=series.day::date GROUP BY series.day ORDER BY series.day`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard charts")
		return
	}
	requestTrend, err := a.adminDailyCounts(ctx, `SELECT TO_CHAR(series.day,'YYYY-MM-DD'),COUNT(l.id)
		FROM generate_series(CURRENT_DATE-INTERVAL '6 days',CURRENT_DATE,INTERVAL '1 day') AS series(day)
		LEFT JOIN request_logs l ON l.created_at::date=series.day::date GROUP BY series.day ORDER BY series.day`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard charts")
		return
	}
	platforms, err := a.adminPlatformCounts(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load dashboard charts")
		return
	}
	requestsByActor, err := a.adminCountBreakdown(ctx,
		`SELECT actor_type,COUNT(*) FROM request_logs WHERE created_at>NOW()-INTERVAL '30 days' GROUP BY actor_type ORDER BY COUNT(*) DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load request analytics")
		return
	}
	responseStatuses, err := a.adminCountBreakdown(ctx,
		`SELECT CASE WHEN status<300 THEN '2xx' WHEN status<400 THEN '3xx' WHEN status<500 THEN '4xx' ELSE '5xx' END,COUNT(*)
		 FROM request_logs WHERE created_at>NOW()-INTERVAL '30 days'
		 GROUP BY CASE WHEN status<300 THEN '2xx' WHEN status<400 THEN '3xx' WHEN status<500 THEN '4xx' ELSE '5xx' END
		 ORDER BY 1`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load request analytics")
		return
	}
	usersByCountry, err := a.adminCountBreakdown(ctx, `
		SELECT COALESCE(NULLIF(BTRIM(country),''),'Not provided'),COUNT(*)
		FROM users GROUP BY 1 ORDER BY COUNT(*) DESC,1 LIMIT 12`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load country analytics")
		return
	}
	usersByAge, err := a.adminCountBreakdown(ctx, `
		SELECT CASE
			WHEN age IS NULL THEN 'Not provided'
			WHEN age<18 THEN 'Under 18'
			WHEN age<25 THEN '18–24'
			WHEN age<35 THEN '25–34'
			WHEN age<45 THEN '35–44'
			WHEN age<55 THEN '45–54'
			ELSE '55+'
		END,COUNT(*) FROM users
		GROUP BY 1
		ORDER BY CASE WHEN MIN(age) IS NULL THEN 99 ELSE MIN(age) END`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load age analytics")
		return
	}
	usersByGender, err := a.adminCountBreakdown(ctx, `
		SELECT CASE WHEN NULLIF(BTRIM(gender),'') IS NULL THEN 'Not provided' ELSE BTRIM(gender) END,COUNT(*)
		FROM users GROUP BY 1 ORDER BY COUNT(*) DESC,1`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load gender analytics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"health": map[string]any{
			"status": "ok", "uptimeSeconds": int64(time.Since(a.startedAt).Seconds()),
			"database": "connected", "databaseSizeBytes": databaseSizeBytes, "databaseConnections": stats.TotalConns(),
			"databaseIdleConnections": stats.IdleConns(), "goroutines": runtime.NumGoroutine(),
			"memoryBytes": memory.Alloc, "checkedAt": time.Now().UTC(),
			"platform": runtimePlatform(), "goVersion": runtime.Version(),
		},
		"counts": map[string]int64{
			"users": users, "books": books, "publicBooks": publicBooks,
			"unresolvedErrors": unresolvedErrors, "requests24h": requests24h,
		},
		"charts": map[string]any{
			"usersByDay": userTrend, "requestsByDay": requestTrend, "platforms": platforms,
			"requestsByActor": requestsByActor, "responseStatuses": responseStatuses,
			"usersByCountry": usersByCountry, "usersByAge": usersByAge, "usersByGender": usersByGender,
		},
	})
}

func (a *API) adminCountBreakdown(ctx context.Context, query string) ([]map[string]any, error) {
	rows, err := a.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var label string
		var count int64
		if err := rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"label": label, "count": count})
	}
	return items, rows.Err()
}

func (a *API) adminDailyCounts(ctx context.Context, query string) ([]map[string]any, error) {
	rows, err := a.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, 7)
	for rows.Next() {
		var date string
		var count int64
		if err := rows.Scan(&date, &count); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"date": date, "count": count})
	}
	return items, rows.Err()
}

func (a *API) adminPlatformCounts(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.db.Query(ctx, `SELECT platform,COUNT(*) FROM users GROUP BY platform ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var platform string
		var count int64
		if err := rows.Scan(&platform, &count); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"platform": platform, "count": count})
	}
	return items, rows.Err()
}

func (a *API) adminRequests(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = value
	}
	actorType := r.URL.Query().Get("actorType")
	if actorType != "" && actorType != "admin" && actorType != "user" && actorType != "anonymous" {
		writeError(w, http.StatusBadRequest, "invalid_actor_type", "actorType must be admin, user, or anonymous")
		return
	}
	rows, err := a.db.Query(r.Context(),
		`SELECT id,method,path,status,duration_ms,created_at,actor_type,actor_id,COALESCE(source_ip::text,''),
		        user_agent,request_bytes,response_bytes
		 FROM request_logs WHERE ($1='' OR actor_type=$1) ORDER BY id DESC LIMIT $2`, actorType, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load request history")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, duration int64
		var method, path, requestActorType, actorID, sourceIP, userAgent string
		var status int
		var requestBytes, responseBytes int64
		var created time.Time
		if err := rows.Scan(&id, &method, &path, &status, &duration, &created, &requestActorType,
			&actorID, &sourceIP, &userAgent, &requestBytes, &responseBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load request history")
			return
		}
		items = append(items, map[string]any{
			"id": id, "method": method, "path": path, "status": status,
			"durationMs": duration, "createdAt": created, "actorType": requestActorType,
			"actorId": actorID, "ipAddress": sourceIP, "userAgent": userAgent,
			"requestBytes": requestBytes, "responseBytes": responseBytes,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load request history")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) adminAlerts(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT id,category,title,details,created_at FROM (
			SELECT id::text,'error'::text AS category,COALESCE(NULLIF(screen,''),'App error') AS title,
			       message AS details,created_at
			FROM error_reports WHERE resolved_at IS NULL
			UNION ALL
			SELECT id::text,'insight'::text,category,message,created_at
			FROM user_feedback WHERE status='new'
			UNION ALL
			SELECT id::text,'server_error'::text,method || ' ' || path,'HTTP ' || status,created_at
			FROM request_logs WHERE status>=500 AND created_at>NOW()-INTERVAL '24 hours'
		) alerts ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin alerts")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, category, title, details string
		var created time.Time
		if err := rows.Scan(&id, &category, &title, &details, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin alerts")
			return
		}
		items = append(items, map[string]any{
			"id": id, "category": category, "title": title, "details": details, "createdAt": created,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load admin alerts")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) adminErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT e.id::text,e.owner_id::text,u.name,u.email,e.message,e.screen,e.platform,e.created_at,e.resolved_at,
		       e.title,e.details,e.reproduction_steps,e.metadata::text,
		       COALESCE((
		         SELECT json_agg(json_build_object(
		           'id',a.id::text,'filename',a.filename,'contentType',a.content_type,'sizeBytes',a.size_bytes
		         ) ORDER BY a.created_at)
		         FROM error_report_attachments a WHERE a.report_id=e.id
		       ),'[]'::json)::text
		FROM error_reports e JOIN users u ON u.id=e.owner_id
		ORDER BY e.created_at DESC LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load error reports")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, ownerID, name, email, message, screen, platform string
		var title, details, steps, metadataJSON, attachmentsJSON string
		var created time.Time
		var resolved *time.Time
		if err := rows.Scan(&id, &ownerID, &name, &email, &message, &screen, &platform, &created, &resolved,
			&title, &details, &steps, &metadataJSON, &attachmentsJSON); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load error reports")
			return
		}
		var metadata map[string]any
		var attachments []map[string]any
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode error report diagnostics")
			return
		}
		if err := json.Unmarshal([]byte(attachmentsJSON), &attachments); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode error report screenshots")
			return
		}
		items = append(items, map[string]any{
			"id": id, "userId": ownerID, "userName": name, "userEmail": email,
			"message": message, "title": title, "details": details, "steps": steps,
			"metadata": metadata, "attachments": attachments, "screen": screen, "platform": platform,
			"createdAt": created, "resolvedAt": resolved,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load error reports")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) adminErrorByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Error report id must be a UUID")
		return
	}
	var input struct {
		Resolved bool `json:"resolved"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var resolvedAt any
	if input.Resolved {
		resolvedAt = time.Now().UTC()
	}
	tag, err := a.db.Exec(r.Context(), `UPDATE error_reports SET resolved_at=$1 WHERE id=$2`, resolvedAt, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update error report")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Error report not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT u.id::text,u.name,u.email,u.created_at,u.updated_at,u.email_verified_at,u.banned_at,u.last_seen_at,u.platform,
		       (SELECT a.created_at FROM user_activity a WHERE a.user_id=u.id ORDER BY a.created_at DESC LIMIT 1),
		       (SELECT l.source_ip::text FROM request_logs l WHERE l.actor_type='user' AND l.actor_id=u.id::text AND l.source_ip IS NOT NULL ORDER BY l.created_at DESC LIMIT 1),
		       (SELECT COUNT(*) FROM records r WHERE r.owner_id=u.id AND r.collection='books' AND r.deleted_at IS NULL),
		       (SELECT COUNT(*) FROM records r WHERE r.owner_id=u.id AND r.collection='writings' AND r.deleted_at IS NULL),
		       (SELECT COUNT(*) FROM files f WHERE f.owner_id=u.id),
		       (SELECT COUNT(*) FROM error_reports e WHERE e.owner_id=u.id)
		FROM users u ORDER BY u.created_at DESC LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load users")
		return
	}
	defer rows.Close()
	users := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, email string
		var created, updated time.Time
		var verified, bannedAt, lastSeen, activity *time.Time
		var platform string
		var lastIP *string
		var books, writings, files, reports int64
		if err := rows.Scan(&id, &name, &email, &created, &updated, &verified, &bannedAt, &lastSeen, &platform, &activity, &lastIP, &books, &writings, &files, &reports); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load users")
			return
		}
		users = append(users, map[string]any{
			"id": id, "name": name, "email": email, "createdAt": created, "updatedAt": updated,
			"emailVerified": verified != nil, "books": books, "writings": writings,
			"files": files, "errorReports": reports, "banned": bannedAt != nil,
			"bannedAt": bannedAt, "lastSeenAt": lastSeen, "platform": platform, "latestActivityAt": activity, "lastIpAddress": lastIP,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load users")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (a *API) adminUserByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "User id must be a UUID")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.adminUserDetails(w, r, id)
	case http.MethodPatch:
		a.updateAdminUser(w, r, id)
	case http.MethodDelete:
		a.deleteAdminUser(w, r, id)
	default:
		methodNotAllowed(w, "GET, PATCH, DELETE")
	}
}

func (a *API) adminUserDetails(w http.ResponseWriter, r *http.Request, id string) {
	var user map[string]any
	var name, email string
	var created, updated time.Time
	var verified, bannedAt, lastSeen *time.Time
	var platform string
	var lastIP *string
	var books, writings int64
	err := a.db.QueryRow(r.Context(),
		`SELECT name,email,created_at,updated_at,email_verified_at,banned_at,last_seen_at,platform,
		 (SELECT l.source_ip::text FROM request_logs l WHERE l.actor_type='user' AND l.actor_id=users.id::text AND l.source_ip IS NOT NULL ORDER BY l.created_at DESC LIMIT 1),
		 (SELECT COUNT(*) FROM records WHERE owner_id=users.id AND collection='books' AND deleted_at IS NULL),
		 (SELECT COUNT(*) FROM records WHERE owner_id=users.id AND collection='writings' AND deleted_at IS NULL)
		 FROM users WHERE id=$1`, id).
		Scan(&name, &email, &created, &updated, &verified, &bannedAt, &lastSeen, &platform, &lastIP, &books, &writings)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "User not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user")
		return
	}
	user = map[string]any{
		"id": id, "name": name, "email": email, "createdAt": created, "updatedAt": updated,
		"emailVerified": verified != nil, "banned": bannedAt != nil, "bannedAt": bannedAt,
		"lastSeenAt": lastSeen, "platform": platform, "books": books, "writings": writings, "lastIpAddress": lastIP,
	}
	rows, err := a.db.Query(r.Context(),
		`SELECT collection,id::text,data,updated_at,deleted_at FROM records WHERE owner_id=$1 ORDER BY collection,updated_at DESC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user records")
		return
	}
	records := make([]map[string]any, 0)
	for rows.Next() {
		var collection, recordID string
		var raw []byte
		var updatedAt time.Time
		var deletedAt *time.Time
		if err := rows.Scan(&collection, &recordID, &raw, &updatedAt, &deletedAt); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user records")
			return
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode user records")
			return
		}
		records = append(records, map[string]any{
			"collection": collection, "id": recordID, "data": data,
			"updatedAt": updatedAt, "deletedAt": deletedAt,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user records")
		return
	}
	rows.Close()
	user["records"] = records
	fileRows, err := a.db.Query(r.Context(),
		`SELECT filename,kind,content_type,size_bytes,created_at FROM files WHERE owner_id=$1 ORDER BY created_at DESC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user files")
		return
	}
	files := make([]map[string]any, 0)
	for fileRows.Next() {
		var filename, kind, contentType string
		var size int64
		var createdAt time.Time
		if err := fileRows.Scan(&filename, &kind, &contentType, &size, &createdAt); err != nil {
			fileRows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user files")
			return
		}
		files = append(files, map[string]any{
			"filename": filename, "kind": kind, "contentType": contentType, "sizeBytes": size, "createdAt": createdAt,
		})
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user files")
		return
	}
	fileRows.Close()
	user["files"] = files
	activityRows, err := a.db.Query(r.Context(),
		`SELECT method,path,status,COALESCE(source_ip::text,''),created_at FROM request_logs
		 WHERE actor_type='user' AND actor_id=$1 ORDER BY created_at DESC LIMIT 100`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user activity")
		return
	}
	activities := make([]map[string]any, 0)
	for activityRows.Next() {
		var method, path, sourceIP string
		var status int
		var createdAt time.Time
		if err := activityRows.Scan(&method, &path, &status, &sourceIP, &createdAt); err != nil {
			activityRows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user activity")
			return
		}
		activities = append(activities, map[string]any{
			"method": method, "path": path, "status": status, "ipAddress": sourceIP, "platform": platform, "createdAt": createdAt,
		})
	}
	if err := activityRows.Err(); err != nil {
		activityRows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user activity")
		return
	}
	activityRows.Close()
	user["activities"] = activities
	writeJSON(w, http.StatusOK, user)
}

func (a *API) updateAdminUser(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Banned   *bool  `json:"banned"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Name == "" || len(input.Name) > 100 || !validEmail(input.Email) {
		writeError(w, http.StatusBadRequest, "invalid_user", "A name up to 100 bytes and a valid email are required")
		return
	}
	if input.Password != "" && (len(input.Password) < 8 || len(input.Password) > 72) {
		writeError(w, http.StatusBadRequest, "invalid_password", "Password must contain 8 to 72 bytes")
		return
	}
	var passwordHash any
	if input.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not update user password")
			return
		}
		passwordHash = string(hash)
	}
	var name, email string
	var created, updated time.Time
	var verified, bannedAt *time.Time
	err := a.db.QueryRow(r.Context(),
		`UPDATE users SET name=$1,email=$2,updated_at=NOW(),password_hash=COALESCE($4,password_hash),
		 email_verified_at=CASE WHEN LOWER(email)<>LOWER($2) THEN NULL ELSE email_verified_at END,
		 email_notifications=CASE WHEN LOWER(email)<>LOWER($2) THEN FALSE ELSE email_notifications END,
		 banned_at=CASE WHEN $3::boolean IS NULL THEN banned_at WHEN $3 THEN COALESCE(banned_at,NOW()) ELSE NULL END,
		 session_revoked_at=CASE WHEN $3 IS TRUE OR $4::text IS NOT NULL THEN NOW() ELSE session_revoked_at END
		 WHERE id=$5
		 RETURNING name,email,created_at,updated_at,email_verified_at,banned_at`,
		input.Name, input.Email, input.Banned, passwordHash, id).Scan(&name, &email, &created, &updated, &verified, &bannedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "email_in_use", "That email belongs to another account")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "User not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "name": name, "email": email, "createdAt": created, "updatedAt": updated,
		"emailVerified": verified != nil, "banned": bannedAt != nil,
	})
}

func (a *API) deleteAdminUser(w http.ResponseWriter, r *http.Request, id string) {
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete user")
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT filename,kind FROM files WHERE owner_id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user uploads")
		return
	}
	files := make([]adminOwnedFile, 0)
	for rows.Next() {
		var file adminOwnedFile
		if err := rows.Scan(&file.filename, &file.kind); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user uploads")
			return
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load user uploads")
		return
	}
	rows.Close()
	tag, err := tx.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete user")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "User not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete user")
		return
	}
	a.removeUnsharedUploads(r.Context(), files)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

type adminOwnedFile struct{ filename, kind string }

func (a *API) removeUnsharedUploads(ctx context.Context, files []adminOwnedFile) {
	removed := make(map[string]struct{})
	for _, file := range files {
		key := file.kind + "\x00" + file.filename
		if _, ok := removed[key]; ok {
			continue
		}
		removed[key] = struct{}{}
		var shared bool
		if err := a.db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM files WHERE kind=$1 AND filename=$2)`,
			file.kind, file.filename).Scan(&shared); err != nil {
			log.Printf("could not check shared upload %q: %v", file.filename, err)
			continue
		}
		if shared {
			continue
		}
		path, err := safeFilePath(a.cfg.uploadDir, file.kind, file.filename)
		if err == nil {
			err = os.Remove(path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		if err != nil {
			log.Printf("could not remove upload %q: %v", file.filename, err)
		}
	}
}

func (a *API) createAdminNotifications(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AllUsers bool     `json:"allUsers"`
		UserIDs  []string `json:"userIds"`
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Channel  string   `json:"channel"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Body = strings.TrimSpace(input.Body)
	if input.Title == "" || len(input.Title) > 160 || input.Body == "" || len(input.Body) > 4000 {
		writeError(w, http.StatusBadRequest, "invalid_notification", "Title (1-160 characters) and message (1-4000 characters) are required")
		return
	}
	if input.Channel == "" {
		input.Channel = "both"
	}
	if input.Channel != "both" && input.Channel != "in_app" && input.Channel != "email" {
		writeError(w, http.StatusBadRequest, "invalid_notification", "channel must be in_app, email, or both")
		return
	}
	if input.AllUsers == (len(input.UserIDs) > 0) || len(input.UserIDs) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_recipients", "Choose all users or provide up to 1000 user IDs")
		return
	}
	var rows pgx.Rows
	var err error
	if input.AllUsers {
		rows, err = a.db.Query(r.Context(), `SELECT id::text,email,email_verified_at IS NOT NULL FROM users ORDER BY id`)
	} else {
		seen := make(map[string]struct{}, len(input.UserIDs))
		for _, id := range input.UserIDs {
			if !validUUID(id) {
				writeError(w, http.StatusBadRequest, "invalid_recipients", "Every recipient id must be a UUID")
				return
			}
			if _, duplicate := seen[id]; duplicate {
				writeError(w, http.StatusBadRequest, "invalid_recipients", "Recipient IDs cannot be duplicated")
				return
			}
			seen[id] = struct{}{}
		}
		rows, err = a.db.Query(r.Context(),
			`SELECT id::text,email,email_verified_at IS NOT NULL FROM users WHERE id=ANY($1::uuid[]) ORDER BY id`,
			input.UserIDs)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notification recipients")
		return
	}
	type recipient struct {
		id, email string
		verified  bool
	}
	recipients := make([]recipient, 0)
	for rows.Next() {
		var item recipient
		if err := rows.Scan(&item.id, &item.email, &item.verified); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notification recipients")
			return
		}
		recipients = append(recipients, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notification recipients")
		return
	}
	rows.Close()
	if len(recipients) == 0 || (!input.AllUsers && len(recipients) != len(input.UserIDs)) {
		writeError(w, http.StatusNotFound, "recipients_not_found", "One or more selected users could not be found")
		return
	}
	emailSent, emailFailed, emailSkipped := 0, 0, 0
	for _, user := range recipients {
		if input.Channel != "email" && input.Channel != "both" {
			continue
		}
		if a.cfg.mailer == nil || !user.verified {
			emailSkipped++
			continue
		}
		if err := a.cfg.mailer.Send(r.Context(), user.email, input.Title, input.Body); err != nil {
			log.Printf("admin notification email delivery failed for user %s: %v", user.id, err)
			emailFailed++
			continue
		}
		emailSent++
	}
	inAppCreated := 0
	if input.Channel == "in_app" || input.Channel == "both" {
		tx, err := a.db.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save notifications")
			return
		}
		for _, user := range recipients {
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO user_notifications(id,user_id,title,body) VALUES($1,$2,$3,$4)`,
				newUUID(), user.id, input.Title, input.Body); err != nil {
				_ = tx.Rollback(r.Context())
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not save notifications")
				return
			}
			inAppCreated++
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not save notifications")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"recipients": len(recipients), "inAppCreated": inAppCreated,
		"emailSent": emailSent, "emailFailed": emailFailed, "emailSkipped": emailSkipped,
	})
}

func (a *API) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, offset := 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 1000000 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be between 0 and 1000000")
			return
		}
		offset = value
	}
	rows, err := a.db.Query(r.Context(),
		`SELECT id::text,title,body,created_at,read_at FROM user_notifications
		 WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`,
		userIDFrom(r), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notifications")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, title, body string
		var created time.Time
		var readAt *time.Time
		if err := rows.Scan(&id, &title, &body, &created, &readAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notifications")
			return
		}
		items = append(items, map[string]any{
			"id": id, "title": title, "body": body, "createdAt": created, "readAt": readAt,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load notifications")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) notifications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listNotifications(w, r)
	case http.MethodDelete:
		tag, err := a.db.Exec(r.Context(), `DELETE FROM user_notifications WHERE user_id=$1`, userIDFrom(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete notifications")
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": tag.RowsAffected()})
	default:
		methodNotAllowed(w, "GET, DELETE")
	}
}

func (a *API) readNotification(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Notification id must be a UUID")
		return
	}
	tag, err := a.db.Exec(r.Context(),
		`UPDATE user_notifications SET read_at=COALESCE(read_at,NOW()) WHERE id=$1 AND user_id=$2`,
		id, userIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not update notification")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Notification not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"read": true})
}

func (a *API) deleteNotification(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Notification id must be a UUID")
		return
	}
	tag, err := a.db.Exec(r.Context(),
		`DELETE FROM user_notifications WHERE id=$1 AND user_id=$2`, id, userIDFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not delete notification")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Notification not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *API) adminBooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.Query(r.Context(), `SELECT id::text,data,created_at,updated_at FROM shelf_public_books ORDER BY updated_at DESC`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public books")
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id string
			var raw []byte
			var created, updated time.Time
			if err := rows.Scan(&id, &raw, &created, &updated); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public books")
				return
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode public book")
				return
			}
			data["id"], data["createdAt"], data["updatedAt"] = id, created, updated
			items = append(items, data)
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		a.createAdminBook(w, r)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) createAdminBook(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title       string `json:"title"`
		Author      string `json:"author"`
		Category    string `json:"category"`
		Description string `json:"description"`
		CoverImage  string `json:"coverImage"`
		CoverColor  string `json:"coverColor"`
		TotalPages  int    `json:"totalPages"`
		FileName    string `json:"fileName"`
		FileMime    string `json:"fileMimeType"`
		CoverName   string `json:"coverFileName"`
		CoverMime   string `json:"coverMimeType"`
	}
	var bookAsset, coverAsset *publicBookAssetData
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(5<<20)+(1<<20))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_book", "Book upload is too large or malformed")
			return
		}
		defer r.MultipartForm.RemoveAll()
		if len(r.MultipartForm.Value["book"]) != 1 || json.Unmarshal([]byte(r.MultipartForm.Value["book"][0]), &input) != nil {
			writeError(w, http.StatusBadRequest, "invalid_book", "Book details are missing or invalid")
			return
		}
		var err error
		if files := r.MultipartForm.File["bookFile"]; len(files) > 1 {
			writeError(w, http.StatusBadRequest, "invalid_book", "Only one book file may be uploaded")
			return
		} else if len(files) == 1 {
			bookAsset, err = readPublicBookAsset(files[0], "books", maxUpload)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_book_file", err.Error())
				return
			}
		}
		if files := r.MultipartForm.File["coverFile"]; len(files) > 1 {
			writeError(w, http.StatusBadRequest, "invalid_book", "Only one cover image may be uploaded")
			return
		} else if len(files) == 1 {
			coverAsset, err = readPublicBookAsset(files[0], "covers", 5<<20)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_cover", err.Error())
				return
			}
		}
	} else if !decodeJSON(w, r, &input) {
		return
	}
	input.Title, input.Author = strings.TrimSpace(input.Title), strings.TrimSpace(input.Author)
	input.Category, input.Description = strings.TrimSpace(input.Category), strings.TrimSpace(input.Description)
	input.CoverImage, input.CoverColor = strings.TrimSpace(input.CoverImage), strings.TrimSpace(input.CoverColor)
	if input.Title == "" || len(input.Title) > 200 || input.Author == "" || len(input.Author) > 200 ||
		len(input.Category) > 100 || len(input.Description) > 10000 || input.TotalPages < 0 || input.TotalPages > 100000 {
		writeError(w, http.StatusBadRequest, "invalid_book", "Provide a title, author, and valid book details")
		return
	}
	if input.CoverColor == "" {
		input.CoverColor = "cover-blue"
	}
	if !validCoverColor(input.CoverColor) {
		writeError(w, http.StatusBadRequest, "invalid_book", "Choose a supported cover color")
		return
	}
	if input.CoverImage != "" && !strings.HasPrefix(input.CoverImage, "https://") && !strings.HasPrefix(input.CoverImage, "/") {
		writeError(w, http.StatusBadRequest, "invalid_book", "Cover image must use HTTPS or a local path")
		return
	}
	now := time.Now().UTC()
	id := newUUID()
	if coverAsset != nil {
		input.CoverImage = "/api/public/books/" + id + "/assets/cover"
		input.CoverName, input.CoverMime = coverAsset.filename, coverAsset.contentType
	}
	if bookAsset != nil {
		input.FileName, input.FileMime = bookAsset.filename, bookAsset.contentType
	}
	data := map[string]any{
		"id": id, "title": input.Title, "author": input.Author, "category": input.Category,
		"description": input.Description, "coverImage": input.CoverImage,
		"coverColor": input.CoverColor, "totalPages": input.TotalPages, "createdAt": now, "updatedAt": now,
		"fileName": input.FileName, "fileMimeType": input.FileMime, "coverFileName": input.CoverName,
		"coverMimeType": input.CoverMime,
	}
	if bookAsset != nil {
		data["publicFileUrl"] = "/api/public/books/" + id + "/assets/book"
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not publish book")
		return
	}
	defer tx.Rollback(r.Context())
	raw, err := json.Marshal(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not encode public book")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO shelf_public_books(id,data,created_at,updated_at) VALUES($1,$2,$3,$3)`,
		id, raw, now); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not publish book")
		return
	}
	for _, asset := range []*publicBookAssetData{bookAsset, coverAsset} {
		if asset == nil {
			continue
		}
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO shelf_public_assets(id,book_id,kind,filename,content_type,size_bytes,data) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			newUUID(), id, asset.kind, asset.filename, asset.contentType, len(asset.data), asset.data); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not store published book files")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not publish book")
		return
	}
	writeJSON(w, http.StatusCreated, data)
}

type publicBookAssetData struct {
	kind, filename, contentType string
	data                        []byte
}

func readPublicBookAsset(header *multipart.FileHeader, kind string, maxSize int64) (*publicBookAssetData, error) {
	extension := strings.ToLower(filepath.Ext(filepath.Base(header.Filename)))
	if !validUploadType(kind, extension) || header.Size < 1 || header.Size > maxSize {
		return nil, fmt.Errorf("File type or size is not supported (maximum %d MiB)", maxSize>>20)
	}
	file, err := header.Open()
	if err != nil {
		return nil, errors.New("Could not read uploaded file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxSize {
		return nil, errors.New("Uploaded file is empty or too large")
	}
	headLen := len(data)
	if headLen > 512 {
		headLen = 512
	}
	detected := http.DetectContentType(data[:headLen])
	if !validDetectedType(extension, detected) && !(extension == ".mobi" && validMobi(data)) {
		return nil, errors.New("File content does not match its extension")
	}
	contentType := mime.TypeByExtension(extension)
	if contentType == "" && extension == ".epub" {
		contentType = "application/epub+zip"
	}
	if contentType == "" && extension == ".mobi" {
		contentType = "application/x-mobipocket-ebook"
	}
	return &publicBookAssetData{
		kind:     map[string]string{"books": "book", "covers": "cover"}[kind],
		filename: filepath.Base(header.Filename), contentType: contentType, data: data,
	}, nil
}

func validCoverColor(value string) bool {
	switch value {
	case "cover-blue", "cover-purple", "cover-red", "cover-green", "cover-teal", "cover-amber", "cover-slate", "cover-rose":
		return true
	default:
		return false
	}
}

func (a *API) publicBookAsset(w http.ResponseWriter, r *http.Request) {
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if !validUUID(id) || (kind != "book" && kind != "cover") {
		writeError(w, http.StatusBadRequest, "invalid_asset", "Published book asset is invalid")
		return
	}
	var filename, contentType string
	var data []byte
	err := a.db.QueryRow(r.Context(),
		`SELECT filename,content_type,data FROM shelf_public_assets WHERE book_id=$1 AND kind=$2`, id, kind).
		Scan(&filename, &contentType, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Published book asset not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load published book asset")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if kind == "book" {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filename)})
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
	}
	_, _ = w.Write(data)
}

func (a *API) adminBookByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Public book id must be a UUID")
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, "DELETE")
		return
	}
	tag, err := a.db.Exec(r.Context(), `DELETE FROM shelf_public_books WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not remove public book")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Public book not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *API) publicBooks(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT data FROM shelf_public_books ORDER BY updated_at DESC LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public books")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var raw []byte
		var item map[string]any
		if err := rows.Scan(&raw); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public books")
			return
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode public book")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public books")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) addPublicBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Public book id must be a UUID")
		return
	}
	var raw []byte
	if err := a.db.QueryRow(r.Context(), `SELECT data FROM shelf_public_books WHERE id=$1`, id).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Public book not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load public book")
		return
	}
	var publicBook map[string]any
	if err := json.Unmarshal(raw, &publicBook); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not decode public book")
		return
	}
	now := time.Now().UTC()
	book := map[string]any{
		"id": newUUID(), "title": publicBook["title"], "author": publicBook["author"],
		"category": publicBook["category"], "description": publicBook["description"],
		"coverImage": publicBook["coverImage"], "coverColor": publicBook["coverColor"],
		"totalPages": publicBook["totalPages"], "currentPage": 0, "status": "unread", "uploadedAt": now,
	}
	if err := validateRequiredFields("books", book); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_public_book", fmt.Sprintf("Published book is invalid: %v", err))
		return
	}
	if err := validateDocument(book); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_public_book", "Published book is invalid")
		return
	}
	rawBook, err := json.Marshal(book)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not encode public book")
		return
	}
	if _, err := a.db.Exec(r.Context(),
		`INSERT INTO records(owner_id,collection,id,data,updated_at) VALUES($1,'books',$2,$3,$4)`,
		userIDFrom(r), book["id"], rawBook, now); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not add public book to library")
		return
	}
	writeJSON(w, http.StatusCreated, book)
}
