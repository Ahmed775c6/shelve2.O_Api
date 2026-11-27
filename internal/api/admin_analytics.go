package api

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"time"
)

type usageWindow struct {
	period        string
	format        string
	currentLabel  string
	previousLabel string
	currentStart  time.Time
	currentEnd    time.Time
	previousStart time.Time
	previousEnd   time.Time
	seriesStart   time.Time
}

func analyticsWindow(period string, now time.Time) (usageWindow, bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	window := usageWindow{period: period, currentEnd: today, format: "YYYY-MM-DD"}
	switch period {
	case "day":
		window.currentLabel = "Last 30 days"
		window.previousLabel = "Previous 30 days"
		window.currentStart = today.AddDate(0, 0, -29)
		window.previousEnd = window.currentStart.AddDate(0, 0, -1)
		window.previousStart = window.previousEnd.AddDate(0, 0, -29)
		window.seriesStart = window.previousStart
	case "month":
		window.format = "YYYY-MM"
		window.currentLabel = "Last 12 months"
		window.previousLabel = "Previous 12 months"
		window.currentStart = today.AddDate(0, -12, 1)
		window.previousEnd = window.currentStart.AddDate(0, 0, -1)
		window.previousStart = window.previousEnd.AddDate(0, -12, 1)
		window.seriesStart = window.previousStart
	case "year":
		window.format = "YYYY"
		window.currentStart = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		window.previousStart = window.currentStart.AddDate(-1, 0, 0)
		window.previousEnd = window.previousStart.AddDate(0, 0, int(today.Sub(window.currentStart).Hours()/24))
		window.currentLabel = "Current year to date"
		window.previousLabel = "Same period last year"
		window.seriesStart = window.previousStart
	default:
		return usageWindow{}, false
	}
	return window, true
}

func (a *API) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "day"
	}
	window, ok := analyticsWindow(period, time.Now().UTC())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_period", "period must be day, month, or year")
		return
	}
	ctx := r.Context()
	if err := a.recordServerDailySnapshot(ctx, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not record server health")
		return
	}
	if period == "year" {
		if err := a.db.QueryRow(ctx,
			`SELECT COALESCE(MIN(usage_day),$1) FROM request_daily_usage`, window.currentStart).
			Scan(&window.seriesStart); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not load usage history")
			return
		}
	}
	current, err := a.usageSummary(ctx, window.currentStart, window.currentEnd)
	if err != nil {
		log.Printf("admin analytics current usage summary failed: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load usage comparison")
		return
	}
	previous, err := a.usageSummary(ctx, window.previousStart, window.previousEnd)
	if err != nil {
		log.Printf("admin analytics previous usage summary failed: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load usage comparison")
		return
	}
	series, err := a.usageSeries(ctx, window)
	if err != nil {
		log.Printf("admin analytics usage series failed: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not load usage history")
		return
	}
	current["label"] = window.currentLabel
	previous["label"] = window.previousLabel
	writeJSON(w, http.StatusOK, map[string]any{
		"period": period,
		"series": series,
		"comparison": map[string]any{
			"current": current, "previous": previous,
			"requestChangePercent": percentChange(current["requests"].(int64), previous["requests"].(int64)),
			"errorChangePercent":   percentChange(current["errors"].(int64), previous["errors"].(int64)),
		},
		"app": map[string]any{
			"platform": runtimePlatform(), "goVersion": runtime.Version(),
			"uptimeSeconds": int64(time.Since(a.startedAt).Seconds()),
		},
	})
}

func percentChange(current, previous int64) float64 {
	if previous == 0 {
		if current == 0 {
			return 0
		}
		return 100
	}
	return (float64(current-previous) / float64(previous)) * 100
}

func (a *API) usageSummary(ctx context.Context, start, end time.Time) (map[string]any, error) {
	var requests, success, redirects, clientErrors, serverErrors, newUsers, avgDuration, maxDuration int64
	err := a.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(request_count),0),COALESCE(SUM(success_count),0),
		       COALESCE(SUM(redirect_count),0),COALESCE(SUM(client_error_count),0),
		       COALESCE(SUM(server_error_count),0),COALESCE(SUM(new_user_count),0),
		       COALESCE(ROUND(SUM(total_duration_ms)::numeric/NULLIF(SUM(request_count),0))::BIGINT,0),
		       COALESCE(MAX(max_duration_ms),0)
		FROM request_daily_usage WHERE usage_day BETWEEN $1 AND $2`, start, end).
		Scan(&requests, &success, &redirects, &clientErrors, &serverErrors, &newUsers, &avgDuration, &maxDuration)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"requests": requests, "success": success, "redirects": redirects,
		"clientErrors": clientErrors, "serverErrors": serverErrors,
		"errors": clientErrors + serverErrors, "newUsers": newUsers,
		"avgDurationMs": avgDuration, "maxDurationMs": maxDuration,
	}, nil
}

func (a *API) usageSeries(ctx context.Context, window usageWindow) ([]map[string]any, error) {
	rows, err := a.db.Query(ctx, `
		SELECT TO_CHAR(date_trunc($1, d.usage_day::timestamp),$2),
		       COALESCE(SUM(d.request_count),0),COALESCE(SUM(d.success_count),0),
		       COALESCE(SUM(d.redirect_count),0),COALESCE(SUM(d.client_error_count),0),
		       COALESCE(SUM(d.server_error_count),0),COALESCE(SUM(d.new_user_count),0),
		       COALESCE(ROUND(SUM(d.total_duration_ms)::numeric/NULLIF(SUM(d.request_count),0))::BIGINT,0),
		       COALESCE(MAX(d.max_duration_ms),0),
		       COALESCE(MAX(s.peak_memory_bytes),0),COALESCE(MAX(s.peak_goroutines),0),
		       COALESCE(MAX(s.peak_database_connections),0),COALESCE(MAX(s.peak_database_size_bytes),0),
		       COALESCE(MAX(s.peak_uptime_seconds),0)
		FROM request_daily_usage d
		LEFT JOIN admin_server_daily s ON s.usage_day=d.usage_day
		WHERE d.usage_day BETWEEN $3 AND $4
		GROUP BY date_trunc($1, d.usage_day::timestamp)
		ORDER BY date_trunc($1, d.usage_day::timestamp)`,
		window.period, window.format, window.seriesStart, window.currentEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var label string
		var requests, success, redirects, clientErrors, serverErrors, newUsers int64
		var avgDuration, maxDuration, memoryBytes, goroutines, dbConnections, databaseSizeBytes, uptime int64
		if err := rows.Scan(&label, &requests, &success, &redirects, &clientErrors, &serverErrors,
			&newUsers, &avgDuration, &maxDuration, &memoryBytes, &goroutines, &dbConnections,
			&databaseSizeBytes, &uptime); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"period": label, "requests": requests, "success": success, "redirects": redirects,
			"clientErrors": clientErrors, "serverErrors": serverErrors,
			"errors": clientErrors + serverErrors, "newUsers": newUsers,
			"avgDurationMs": avgDuration, "maxDurationMs": maxDuration,
			"peakMemoryBytes": memoryBytes, "peakGoroutines": goroutines,
			"peakDatabaseConnections": dbConnections, "peakDatabaseSizeBytes": databaseSizeBytes,
			"peakUptimeSeconds": uptime,
		})
	}
	return items, rows.Err()
}
