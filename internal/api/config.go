package api

import (
	"net/http"
	"time"

	"github.com/AhmedA3001894/Shelve_Api.git/internal/mailer"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	JWTSecret           []byte
	UploadDir           string
	AnthropicKey        string
	Origins             map[string]struct{}
	Mailer              mailer.Sender
	VerificationBaseURL string
	AdminUsername       string
	AdminPassword       string
	DeveloperUsername   string
	DeveloperPassword   string
}

type config struct {
	jwtSecret           []byte
	uploadDir           string
	anthropicKey        string
	origins             map[string]struct{}
	mailer              mailer.Sender
	verificationBaseURL string
	adminUsername       string
	adminPassword       string
	developerUsername   string
	developerPassword   string
}

func New(db *pgxpool.Pool, cfg Config) *API {
	return newAPI(db, config{
		jwtSecret:           cfg.JWTSecret,
		uploadDir:           cfg.UploadDir,
		anthropicKey:        cfg.AnthropicKey,
		origins:             cfg.Origins,
		mailer:              cfg.Mailer,
		verificationBaseURL: cfg.VerificationBaseURL,
		adminUsername:       cfg.AdminUsername,
		adminPassword:       cfg.AdminPassword,
		developerUsername:   cfg.DeveloperUsername,
		developerPassword:   cfg.DeveloperPassword,
	})
}

func (a *API) Handler() http.Handler {
	return a.handler()
}

func newAPI(db *pgxpool.Pool, cfg config) *API {
	if cfg.adminUsername == "" {
		cfg.adminUsername = "Ahmed"
	}
	return &API{
		db: db, cfg: cfg, httpClient: &http.Client{Timeout: 25 * time.Second},
		rate: make(map[string][]time.Time), startedAt: time.Now(),
	}
}
