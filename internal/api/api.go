package api

import (
	"embed"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	maxJSONBody             = 1 << 20
	maxUpload               = 20 << 20
	maxErrorReportMessage   = 4000
	maxErrorReportScreen    = 64
	tokenIssuer             = "shelve-api"
	tokenLifetime           = 24 * time.Hour
	rememberedTokenLifetime = 30 * 24 * time.Hour
	legacyTokenLifetime     = 2 * time.Hour
	adminTokenLifetime      = 12 * time.Hour
)

//go:embed usage_next/out/*.html usage_next/out/*.ico usage_next/out/*.svg usage_next/out/*.txt usage_next/out/_next
var usageFiles embed.FS

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var archiveIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

var apiResources = map[string]string{
	"books": "books", "notes": "notes", "writings": "writings",
	"reading-plans": "readingPlans", "reading-sessions": "readingSessions",
	"writing-sessions": "writingSessions",
}

var syncResources = map[string]string{
	"books": "books", "writings": "writings", "readingPlans": "readingPlans",
	"readingSessions": "readingSessions", "writingSessions": "writingSessions",
	"notes": "notes", "progress": "progress",
}

var syncResourceOrder = []string{
	"books", "progress", "notes", "readingSessions", "readingPlans",
	"writings", "writingSessions",
}

type API struct {
	db         *pgxpool.Pool
	cfg        config
	httpClient *http.Client
	rateMu     sync.Mutex
	rate       map[string][]time.Time
	startedAt  time.Time
	requestSeq uint64
}

type contextKey string

const userContextKey contextKey = "userID"

func (a *API) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/auth/signup", methodGate("POST", http.HandlerFunc(a.signup)))
	mux.Handle("/api/auth/signin", methodGate("POST", http.HandlerFunc(a.signin)))
	mux.Handle("/api/auth/verify-email", methodGate("POST", http.HandlerFunc(a.verifyEmail)))
	mux.Handle("/api/auth/verification/resend", methodGate("POST", http.HandlerFunc(a.resendVerification)))
	mux.Handle("/api/auth/password/forgot", methodGate("POST", http.HandlerFunc(a.forgotPassword)))
	mux.Handle("/api/auth/password/reset", methodGate("POST", http.HandlerFunc(a.resetPassword)))
	mux.Handle("/api/auth/me", a.auth(methodGate("GET", http.HandlerFunc(a.me))))
	mux.Handle("/api/auth/profile", a.auth(methodGate("PATCH", http.HandlerFunc(a.updateProfile))))
	mux.Handle("/api/auth/session", a.auth(methodGate("PATCH", http.HandlerFunc(a.updateSession))))
	mux.Handle("/api/auth/password/verify", a.auth(methodGate("POST", http.HandlerFunc(a.verifyPassword))))
	mux.Handle("/api/auth/password", a.auth(methodGate("PATCH", http.HandlerFunc(a.changePassword))))
	mux.Handle("/api/auth/account/delete", a.auth(methodGate("POST", http.HandlerFunc(a.deleteAccount))))
	mux.Handle("/api/auth/email-notifications", a.auth(http.HandlerFunc(a.emailNotificationPreferences)))
	mux.Handle("/api/error-reports", a.auth(methodGate("POST", http.HandlerFunc(a.createErrorReport))))
	mux.Handle("/api/reports", a.auth(methodGate("POST", http.HandlerFunc(a.createUserFeedback))))
	mux.Handle("/api/notifications", a.auth(http.HandlerFunc(a.notifications)))
	mux.Handle("/api/notifications/{id}/read", a.auth(methodGate("POST", http.HandlerFunc(a.readNotification))))
	mux.Handle("/api/notifications/{id}", a.auth(methodGate("DELETE", http.HandlerFunc(a.deleteNotification))))
	mux.Handle("/api/admin/login", methodGate("POST", http.HandlerFunc(a.adminLogin)))
	mux.Handle("/api/admin/me", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminMe))))
	mux.Handle("/api/admin/dashboard", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminDashboard))))
	mux.Handle("/api/admin/analytics", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminAnalytics))))
	mux.Handle("/api/admin/notes", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminNotes))))
	mux.Handle("/api/admin/notes/{id}", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminNoteByID))))
	mux.Handle("/api/admin/profile", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminProfileRoute))))
	mux.Handle("/api/admin/profile/password", a.adminAuth(a.adminReauth(methodGate("PATCH", http.HandlerFunc(a.changeAdminPassword)))))
	mux.Handle("/api/admin/requests", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminRequests))))
	mux.Handle("/api/admin/errors", a.adminAuth(http.HandlerFunc(a.adminErrors)))
	mux.Handle("/api/admin/errors/{id}", a.adminAuth(a.adminReauth(methodGate("PATCH", http.HandlerFunc(a.adminErrorByID)))))
	mux.Handle("/api/admin/errors/{id}/attachments/{attachmentId}", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminErrorAttachment))))
	mux.Handle("/api/admin/reports", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminFeedback))))
	mux.Handle("/api/admin/reports/{id}", a.adminAuth(a.adminReauth(methodGate("PATCH", http.HandlerFunc(a.adminFeedbackByID)))))
	mux.Handle("/api/admin/users", a.adminAuth(http.HandlerFunc(a.adminUsers)))
	mux.Handle("/api/admin/users/{id}", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminUserByID))))
	mux.Handle("/api/admin/notifications", a.adminAuth(a.adminReauth(methodGate("POST", http.HandlerFunc(a.createAdminNotifications)))))
	mux.Handle("/api/admin/books", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminBooks))))
	mux.Handle("/api/admin/books/{id}", a.adminAuth(a.adminReauth(http.HandlerFunc(a.adminBookByID))))
	mux.Handle("/api/admin/alerts", a.adminAuth(methodGate("GET", http.HandlerFunc(a.adminAlerts))))
	mux.Handle("/api/public/books", methodGate("GET", http.HandlerFunc(a.publicBooks)))
	mux.Handle("/api/public/books/{id}/add", a.auth(methodGate("POST", http.HandlerFunc(a.addPublicBook))))
	mux.Handle("/api/public/books/{id}/assets/{kind}", methodGate("GET", http.HandlerFunc(a.publicBookAsset)))
	mux.Handle("/api/public/openlibrary-pdf/{iaId}", methodGate("GET", http.HandlerFunc(a.openLibraryPDF)))
	mux.HandleFunc("/usage", a.serveUsage)
	mux.HandleFunc("/usage/", a.serveUsage)
	mux.Handle("/api/public/writings", methodGate("GET", http.HandlerFunc(a.publicWritings)))

	mux.Handle("/api/books", a.auth(http.HandlerFunc(a.books)))
	mux.Handle("/api/books/{id}", a.auth(http.HandlerFunc(a.bookByID)))
	mux.Handle("/api/books/{id}/notes", a.auth(http.HandlerFunc(a.bookNotes)))
	mux.Handle("/api/notes", a.auth(http.HandlerFunc(a.resourceCollection("notes"))))
	mux.Handle("/api/notes/{id}", a.auth(http.HandlerFunc(a.resourceItem("notes"))))
	mux.Handle("/api/progress/{bookId}", a.auth(http.HandlerFunc(a.progress)))
	mux.Handle("/api/writings", a.auth(http.HandlerFunc(a.resourceCollection("writings"))))
	mux.Handle("/api/writings/{id}", a.auth(http.HandlerFunc(a.resourceItem("writings"))))
	mux.Handle("/api/reading-plans", a.auth(http.HandlerFunc(a.resourceCollection("reading-plans"))))
	mux.Handle("/api/reading-plans/{id}", a.auth(http.HandlerFunc(a.resourceItem("reading-plans"))))
	mux.Handle("/api/reading-sessions", a.auth(http.HandlerFunc(a.resourceCollection("reading-sessions"))))
	mux.Handle("/api/reading-sessions/{id}", a.auth(http.HandlerFunc(a.resourceItem("reading-sessions"))))
	mux.Handle("/api/writing-sessions", a.auth(http.HandlerFunc(a.resourceCollection("writing-sessions"))))
	mux.Handle("/api/writing-sessions/{id}", a.auth(http.HandlerFunc(a.resourceItem("writing-sessions"))))
	mux.Handle("/api/sync/import-json", a.auth(methodGate("POST", http.HandlerFunc(a.importJSON))))
	mux.Handle("/api/sync/merge", a.auth(methodGate("POST", http.HandlerFunc(a.importJSON))))
	mux.Handle("/api/sync/replace", a.auth(methodGate("POST", http.HandlerFunc(a.replaceAllData))))
	mux.Handle("/api/sync/pull", a.auth(methodGate("GET", http.HandlerFunc(a.pull))))
	mux.Handle("/api/sync/conflicts", a.auth(http.HandlerFunc(a.syncConflicts)))
	mux.Handle("/api/upload", a.auth(methodGate("POST", http.HandlerFunc(a.upload))))
	mux.Handle("/api/files/{type}/{filename}", a.auth(methodGate("GET", http.HandlerFunc(a.download))))
	mux.Handle("/api/files/cleanup-orphans", a.auth(methodGate("POST", http.HandlerFunc(a.cleanupOrphanFiles))))
	mux.Handle("/api/ai/chat", a.auth(methodGate("POST", http.HandlerFunc(a.aiChat))))
	mux.Handle("/api/ai/writing-assist", a.auth(methodGate("POST", http.HandlerFunc(a.aiWritingAssist))))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
	})
	return a.requestLogger(a.cors(mux))
}
