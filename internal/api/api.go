package api

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	maxJSONBody           = 1 << 20
	maxUpload             = 20 << 20
	maxErrorReportMessage = 4000
	maxErrorReportScreen  = 64
	tokenIssuer           = "shelve-api"
	tokenLifetime         = 720 * time.Hour
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

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
}

type contextKey string

const userContextKey contextKey = "userID"

func (a *API) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/auth/signup", methodGate("POST", http.HandlerFunc(a.signup)))
	mux.Handle("/api/auth/signin", methodGate("POST", http.HandlerFunc(a.signin)))
	mux.Handle("/api/auth/verify-email", methodGate("POST", http.HandlerFunc(a.verifyEmail)))
	mux.Handle("/api/auth/verification/resend", methodGate("POST", http.HandlerFunc(a.resendVerification)))
	mux.Handle("/api/auth/me", a.auth(methodGate("GET", http.HandlerFunc(a.me))))
	mux.Handle("/api/auth/password", a.auth(methodGate("PATCH", http.HandlerFunc(a.changePassword))))
	mux.Handle("/api/auth/email-notifications", a.auth(http.HandlerFunc(a.emailNotificationPreferences)))
	mux.Handle("/api/error-reports", a.auth(methodGate("POST", http.HandlerFunc(a.createErrorReport))))
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
	return a.cors(mux)
}
