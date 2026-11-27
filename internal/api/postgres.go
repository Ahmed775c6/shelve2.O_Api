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
		ALTER TABLE users ADD COLUMN IF NOT EXISTS banned_at TIMESTAMPTZ;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS session_revoked_at TIMESTAMPTZ;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS platform TEXT NOT NULL DEFAULT 'Unknown';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS age SMALLINT;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS gender TEXT NOT NULL DEFAULT '';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT '';
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
		CREATE TABLE IF NOT EXISTS password_reset_tokens (
			user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS password_reset_tokens_expiry_idx
			ON password_reset_tokens(expires_at);
		CREATE INDEX IF NOT EXISTS email_verification_tokens_expiry_idx
			ON email_verification_tokens(expires_at);
		CREATE TABLE IF NOT EXISTS email_notification_sends (
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			event_type TEXT NOT NULL,
			event_key TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY(owner_id,event_type,event_key)
		);
		CREATE TABLE IF NOT EXISTS request_logs (
			id BIGSERIAL PRIMARY KEY,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			status INTEGER NOT NULL,
			duration_ms BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			actor_type TEXT NOT NULL DEFAULT 'anonymous',
			actor_id TEXT NOT NULL DEFAULT '',
			source_ip INET,
			user_agent TEXT NOT NULL DEFAULT '',
			request_bytes BIGINT NOT NULL DEFAULT 0,
			response_bytes BIGINT NOT NULL DEFAULT 0
		);
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS actor_type TEXT NOT NULL DEFAULT 'anonymous';
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS actor_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS source_ip INET;
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '';
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS request_bytes BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS response_bytes BIGINT NOT NULL DEFAULT 0;
		CREATE INDEX IF NOT EXISTS request_logs_created_idx ON request_logs(created_at DESC);
		CREATE INDEX IF NOT EXISTS request_logs_actor_idx ON request_logs(actor_type,actor_id,created_at DESC);
		CREATE TABLE IF NOT EXISTS request_daily_usage (
			usage_day DATE PRIMARY KEY,
			request_count BIGINT NOT NULL DEFAULT 0,
			success_count BIGINT NOT NULL DEFAULT 0,
			redirect_count BIGINT NOT NULL DEFAULT 0,
			client_error_count BIGINT NOT NULL DEFAULT 0,
			server_error_count BIGINT NOT NULL DEFAULT 0,
			new_user_count BIGINT NOT NULL DEFAULT 0,
			total_duration_ms BIGINT NOT NULL DEFAULT 0,
			max_duration_ms BIGINT NOT NULL DEFAULT 0
		);
		ALTER TABLE request_daily_usage ADD COLUMN IF NOT EXISTS new_user_count BIGINT NOT NULL DEFAULT 0;
		INSERT INTO request_daily_usage(
			usage_day,request_count,success_count,redirect_count,client_error_count,server_error_count,total_duration_ms,max_duration_ms
		)
		SELECT created_at::date,COUNT(*),
		       COUNT(*) FILTER (WHERE status BETWEEN 200 AND 299),
		       COUNT(*) FILTER (WHERE status BETWEEN 300 AND 399),
		       COUNT(*) FILTER (WHERE status BETWEEN 400 AND 499),
		       COUNT(*) FILTER (WHERE status>=500),
		       COALESCE(SUM(duration_ms),0),COALESCE(MAX(duration_ms),0)
		FROM request_logs GROUP BY created_at::date
		ON CONFLICT(usage_day) DO UPDATE SET
			request_count=EXCLUDED.request_count,success_count=EXCLUDED.success_count,
			redirect_count=EXCLUDED.redirect_count,client_error_count=EXCLUDED.client_error_count,
			server_error_count=EXCLUDED.server_error_count,total_duration_ms=EXCLUDED.total_duration_ms,
			max_duration_ms=EXCLUDED.max_duration_ms;
		INSERT INTO request_daily_usage(usage_day,new_user_count)
		SELECT created_at::date,COUNT(*) FROM users GROUP BY created_at::date
		ON CONFLICT(usage_day) DO UPDATE SET
			new_user_count=GREATEST(request_daily_usage.new_user_count,EXCLUDED.new_user_count);
		CREATE TABLE IF NOT EXISTS admin_server_daily (
			usage_day DATE PRIMARY KEY,
			peak_memory_bytes BIGINT NOT NULL DEFAULT 0,
			peak_goroutines BIGINT NOT NULL DEFAULT 0,
			peak_database_connections BIGINT NOT NULL DEFAULT 0,
			peak_database_size_bytes BIGINT NOT NULL DEFAULT 0,
			peak_uptime_seconds BIGINT NOT NULL DEFAULT 0,
			last_observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE admin_server_daily ADD COLUMN IF NOT EXISTS peak_database_size_bytes BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ;
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS platform TEXT NOT NULL DEFAULT 'Unknown';
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS details TEXT NOT NULL DEFAULT '';
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS reproduction_steps TEXT NOT NULL DEFAULT '';
		ALTER TABLE error_reports ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
		CREATE TABLE IF NOT EXISTS error_report_attachments (
			id UUID PRIMARY KEY,
			report_id UUID NOT NULL REFERENCES error_reports(id) ON DELETE CASCADE,
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			filename TEXT NOT NULL,
			content_type TEXT NOT NULL CHECK (content_type IN ('image/png','image/jpeg','image/webp')),
			size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 5242880),
			data BYTEA NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS error_report_attachments_report_idx ON error_report_attachments(report_id,created_at);
		CREATE TABLE IF NOT EXISTS user_activity (
			id BIGSERIAL PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			status INTEGER NOT NULL,
			platform TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS user_activity_user_created_idx ON user_activity(user_id,created_at DESC);
		CREATE TABLE IF NOT EXISTS user_feedback (
			id UUID PRIMARY KEY,
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			category TEXT NOT NULL,
			message TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','reviewing','resolved')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS user_feedback_created_idx ON user_feedback(created_at DESC);
		CREATE TABLE IF NOT EXISTS user_notifications (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			read_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS user_notifications_user_created_idx
			ON user_notifications(user_id,created_at DESC);
		CREATE TABLE IF NOT EXISTS admin_profiles (
			profile_key TEXT PRIMARY KEY CHECK (profile_key='primary'),
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			avatar_data_url TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS admin_notes (
			id UUID PRIMARY KEY,
			admin_name TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS admin_notes_owner_updated_idx ON admin_notes(admin_name,updated_at DESC);
		INSERT INTO schema_migrations(version) VALUES (4) ON CONFLICT DO NOTHING;
		CREATE TABLE IF NOT EXISTS shelf_public_books (
			id UUID PRIMARY KEY,
			data JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS shelf_public_books_updated_idx ON shelf_public_books(updated_at DESC);
		CREATE TABLE IF NOT EXISTS shelf_public_assets (
			id UUID PRIMARY KEY,
			book_id UUID NOT NULL REFERENCES shelf_public_books(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('book','cover')),
			filename TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes BIGINT NOT NULL,
			data BYTEA NOT NULL,
			UNIQUE(book_id,kind)
		);
	`)
	return err
}
