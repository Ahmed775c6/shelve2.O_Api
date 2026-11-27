package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigEmailDeliveryConfiguration(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("ADMIN_PASSWORD", "a-test-admin-password")
	t.Setenv("ADMIN_USERNAME", "Ahmed")
	t.Setenv("DEVELOPER_USERNAME", "")
	t.Setenv("DEVELOPER_PASSWORD", "")
	t.Setenv("PORT", "")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("UPLOAD_DIR", "")
	t.Setenv("POSTGRES_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_PORT", "")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SMTP_FROM", "")
	t.Setenv("EMAIL_VERIFICATION_BASE_URL", "")

	if _, err := loadConfig(); err != nil {
		t.Fatalf("email delivery should be optional: %v", err)
	}

	t.Setenv("SMTP_HOST", "smtp.example.test")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected incomplete email configuration to fail")
	}

	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_FROM", "Shelve <noreply@example.test>")
	t.Setenv("EMAIL_VERIFICATION_BASE_URL", "https://shelve.example.test/verify-email")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("valid email configuration rejected: %v", err)
	}
	if cfg.mailer == nil {
		t.Fatal("valid email configuration did not create an SMTP sender")
	}
}

func TestLoadDotEnvFindsModuleRoot(t *testing.T) {
	previousValue, wasSet := os.LookupEnv("SHELVE_DOTENV_TEST")
	if err := os.Unsetenv("SHELVE_DOTENV_TEST"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("SHELVE_DOTENV_TEST", previousValue)
		} else {
			_ = os.Unsetenv("SHELVE_DOTENV_TEST")
		}
	})

	moduleRoot := t.TempDir()
	workingDir := filepath.Join(moduleRoot, "cmd", "shelve")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, ".env"), []byte("SHELVE_DOTENV_TEST=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	if err := loadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("SHELVE_DOTENV_TEST"); got != "loaded" {
		t.Fatalf("SHELVE_DOTENV_TEST = %q, want %q", got, "loaded")
	}
}
