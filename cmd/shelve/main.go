package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	httpapi "github.com/AhmedA3001894/Shelve_Api.git/internal/api"
	"github.com/AhmedA3001894/Shelve_Api.git/internal/mailer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	if err := loadDotEnv(); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(connectCtx, cfg.postgresURL)
	cancelConnect()
	if err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	err = pool.Ping(pingCtx)
	cancelPing()
	if err != nil {
		pool.Close()
		log.Fatalf("connect to PostgreSQL: %v", err)
	}
	defer pool.Close()

	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), 30*time.Second)
	err = httpapi.MigratePostgres(migrateCtx, pool)
	cancelMigrate()
	if err != nil {
		log.Fatalf("initialize PostgreSQL schema: %v", err)
	}

	if err := os.MkdirAll(cfg.uploadDir, 0o750); err != nil {
		log.Fatalf("create uploads directory: %v", err)
	}

	server := &http.Server{
		Addr: ":" + cfg.port,
		Handler: httpapi.New(pool, httpapi.Config{
			JWTSecret:           cfg.jwtSecret,
			UploadDir:           cfg.uploadDir,
			AnthropicKey:        cfg.anthropicKey,
			Origins:             cfg.origins,
			Mailer:              cfg.mailer,
			VerificationBaseURL: cfg.verificationBaseURL,
			AdminUsername:       cfg.adminUsername,
			AdminPassword:       cfg.adminPassword,
			DeveloperUsername:   cfg.developerUsername,
			DeveloperPassword:   cfg.developerPassword,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Shelve API listening on :%s", cfg.port)
		serverErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server: %v", err)
		}
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	}
}

func loadDotEnv() error {
	if err := godotenv.Load(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}
	for dir := workingDir; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			envPath := filepath.Join(dir, ".env")
			if err := godotenv.Load(envPath); !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
	}
}

type config struct {
	postgresURL         string
	jwtSecret           []byte
	port                string
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

func loadConfig() (config, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if len(secret) < 32 {
		return config{}, errors.New("JWT_SECRET is required and must be at least 32 characters")
	}
	adminPassword := os.Getenv("ADMIN_PASSWORD")
	if len(adminPassword) < 12 {
		return config{}, errors.New("ADMIN_PASSWORD is required and must be at least 12 characters")
	}
	adminUsername := envOr("ADMIN_USERNAME", "Ahmed")
	if strings.TrimSpace(adminUsername) == "" {
		return config{}, errors.New("ADMIN_USERNAME cannot be empty")
	}
	developerUsername := strings.TrimSpace(os.Getenv("DEVELOPER_USERNAME"))
	developerPassword := os.Getenv("DEVELOPER_PASSWORD")
	if (developerUsername == "") != (developerPassword == "") {
		return config{}, errors.New("DEVELOPER_USERNAME and DEVELOPER_PASSWORD must be configured together")
	}
	if developerPassword != "" && len(developerPassword) < 12 {
		return config{}, errors.New("DEVELOPER_PASSWORD must be at least 12 characters")
	}
	if developerUsername != "" && strings.EqualFold(developerUsername, adminUsername) {
		return config{}, errors.New("DEVELOPER_USERNAME must differ from ADMIN_USERNAME")
	}
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return config{}, errors.New("PORT must be a valid TCP port")
	}

	origins := strings.Split(os.Getenv("CORS_ORIGINS"), ",")
	if strings.TrimSpace(os.Getenv("CORS_ORIGINS")) == "" {
		origins = []string{
			"tauri://localhost",
			"http://tauri.localhost",
			"http://localhost:1420",
			"http://127.0.0.1:1420",
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		}
	}
	allowedOrigins := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			allowedOrigins[origin] = struct{}{}
		}
	}

	uploadDir := strings.TrimSpace(os.Getenv("UPLOAD_DIR"))
	if uploadDir == "" {
		uploadDir = "./uploads"
	}
	smtpHost := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	smtpPortValue := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	smtpUser := os.Getenv("SMTP_USERNAME")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	smtpFrom := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	verificationBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("EMAIL_VERIFICATION_BASE_URL")), "/")
	var emailSender mailer.Sender
	if smtpHost != "" || smtpPortValue != "" || smtpUser != "" || smtpPassword != "" || smtpFrom != "" || verificationBaseURL != "" {
		if smtpHost == "" || smtpFrom == "" || verificationBaseURL == "" {
			return config{}, errors.New("SMTP_HOST, SMTP_FROM, and EMAIL_VERIFICATION_BASE_URL are required to enable email")
		}
		verificationURL, err := url.ParseRequestURI(verificationBaseURL)
		if err != nil || (verificationURL.Scheme != "https" && verificationURL.Scheme != "http") || verificationURL.Host == "" {
			return config{}, errors.New("EMAIL_VERIFICATION_BASE_URL must be an absolute HTTP or HTTPS URL")
		}
		smtpPort := 587
		if smtpPortValue != "" {
			smtpPort, err = strconv.Atoi(smtpPortValue)
			if err != nil || smtpPort < 1 || smtpPort > 65535 {
				return config{}, errors.New("SMTP_PORT must be between 1 and 65535")
			}
		}
		emailSender, err = mailer.NewSMTP(smtpHost, smtpPort, smtpUser, smtpPassword, smtpFrom)
		if err != nil {
			return config{}, fmt.Errorf("configure email delivery: %w", err)
		}
	}
	return config{
		postgresURL:         envOr("POSTGRES_URL", "postgres://shelve:shelve@localhost:5432/shelve?sslmode=disable"),
		jwtSecret:           []byte(secret),
		port:                port,
		uploadDir:           uploadDir,
		anthropicKey:        strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")),
		origins:             allowedOrigins,
		mailer:              emailSender,
		verificationBaseURL: verificationBaseURL,
		adminUsername:       adminUsername,
		adminPassword:       adminPassword,
		developerUsername:   developerUsername,
		developerPassword:   developerPassword,
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
