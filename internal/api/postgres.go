package api

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func MigratePostgres(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS users_email_key ON users (LOWER(email));
		CREATE TABLE IF NOT EXISTS records (
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			collection TEXT NOT NULL,
			id UUID NOT NULL,
			data JSONB NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			deleted_at TIMESTAMPTZ,
			PRIMARY KEY (owner_id, collection, id)
		);
		CREATE INDEX IF NOT EXISTS records_owner_collection_updated_idx
			ON records (owner_id, collection, updated_at);
		CREATE TABLE IF NOT EXISTS files (
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			filename TEXT NOT NULL,
			kind TEXT NOT NULL,
			upload_id UUID,
			content_type TEXT NOT NULL,
			size_bytes BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (owner_id, filename)
		);
		CREATE TABLE IF NOT EXISTS error_reports (
			id UUID PRIMARY KEY,
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			message TEXT NOT NULL,
			screen TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL
		);
		ALTER TABLE files ADD COLUMN IF NOT EXISTS upload_id UUID;
		CREATE TABLE IF NOT EXISTS sync_conflicts (
			id UUID PRIMARY KEY,
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			collection TEXT NOT NULL,
			record_id UUID NOT NULL,
			local_data JSONB,
			remote_data JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			resolved_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS sync_conflicts_owner_unresolved_idx
			ON sync_conflicts(owner_id,resolved_at);
		CREATE UNIQUE INDEX IF NOT EXISTS sync_conflicts_one_unresolved_record_idx
			ON sync_conflicts(owner_id,collection,record_id) WHERE resolved_at IS NULL;
		INSERT INTO schema_migrations(version) VALUES (1) ON CONFLICT DO NOTHING;
		INSERT INTO schema_migrations(version) VALUES (2) ON CONFLICT DO NOTHING;
		CREATE UNIQUE INDEX IF NOT EXISTS files_upload_idempotency_idx
			ON files(owner_id,kind,upload_id);
		ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS email_notifications BOOLEAN NOT NULL DEFAULT FALSE;
		DO $migration$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version=3) THEN
				UPDATE users SET email_verified_at=created_at WHERE email_verified_at IS NULL;
				INSERT INTO schema_migrations(version) VALUES (3);
			END IF;
		END
		$migration$;
		CREATE TABLE IF NOT EXISTS email_verification_tokens (
			user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS email_verification_tokens_expiry_idx
			ON email_verification_tokens(expires_at);
		CREATE TABLE IF NOT EXISTS email_notification_sends (
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			event_type TEXT NOT NULL,
			event_key TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY(owner_id,event_type,event_key)
		);
	`)
	return err
}
