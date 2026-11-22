# Shelve Go API

REST API for Shelve backed by PostgreSQL. Configure `JWT_SECRET` (at least 32
characters) before starting; `.env` is loaded from the working directory or
the Go API module root when present. See `.env.example`.

The backend is organized by runtime and responsibility:

- `cmd/shelve`: process startup, environment configuration, and HTTP server
  lifecycle.
- `internal/api`: HTTP routes, auth, records, sync, uploads, AI, PostgreSQL
  migrations, and shared HTTP helpers.
- `internal/mailer`: SMTP transport and delivery.

```sh
go test ./...
go build ./...
go run ./cmd/shelve
```

PostgreSQL uses `POSTGRES_URL`
(default `postgres://shelve:shelve@localhost:5432/shelve?sslmode=disable`).
The service checks PostgreSQL and applies versioned SQL migrations before it
starts listening. Uploads are stored under `./uploads` by default.

When email delivery is configured, signup requires a verified email address
before the first sign-in. Without email delivery configured, users can sign up
and sign in while their accounts remain marked unverified; verification is
enforced once email delivery is enabled. Configure `SMTP_HOST`, `SMTP_FROM`,
and `EMAIL_VERIFICATION_BASE_URL` together to enable email delivery; the
verification URL should point to the frontend's `/verify-email` route.
`SMTP_PORT` defaults to 587, and credentials are optional for relays that do
not require authentication. The SMTP sender requires STARTTLS. See
`.env.example` for the variable names.

Email notifications are disabled by default. Verified users can opt in to
actionable sync-conflict and account-security emails from Settings.

All data and file routes require `Authorization: Bearer <token>`. Signup and
signin return a 720-hour HMAC JWT after email verification. The Web frontend and
desktop app should use the same API origin (`VITE_API_BASE_URL`); set
`CORS_ORIGINS` to include the Web app's origin. API errors use
`{"error":{"code":"...","message":"..."}}`.

Routes:

- `POST /api/auth/signup`, `POST /api/auth/signin`, `GET /api/auth/me`
- `POST /api/auth/verify-email`, `POST /api/auth/verification/resend`
- `GET|PATCH /api/auth/email-notifications`, `PATCH /api/auth/password`
- `GET|POST /api/books`, `GET|PATCH|DELETE /api/books/{id}`
- `GET|POST /api/books/{id}/notes`, CRUD `/api/notes`
- `GET|PATCH /api/progress/{bookId}`
- CRUD collections: `/api/writings`, `/api/reading-plans`,
  `/api/reading-sessions` (updates/deletes use `/{id}`)
- `POST /api/sync/merge` (also available as `/api/sync/import-json`),
  `GET /api/sync/pull`
- `GET|POST /api/sync/conflicts`
- `POST /api/upload`, `GET /api/files/{type}/{filename}`
- `POST /api/ai/chat`, `POST /api/ai/writing-assist`
- `POST /api/error-reports` (authenticated; accepts an error `message` and optional
  app `screen`; the server associates reports with the verified account ID)

Sync JSON uses `books`, `writings`, `readingPlans`, `readingSessions`, `notes`,
and `progress` arrays. Imported UUID records are merged only when their
`updatedAt` is newer than the stored version. AI routes return an explicit
unavailable response until `ANTHROPIC_API_KEY` is configured.
