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
verification URL should point to the frontend's `/verify-email` route. The
same email configuration sends one-hour password-reset links to
`/reset-password`.
`SMTP_PORT` defaults to 587, and credentials are optional for relays that do
not require authentication. The SMTP sender requires STARTTLS. See
`.env.example` for the variable names.

The operational console is served by the API at `/usage`. Set
`ADMIN_PASSWORD` to a unique password of at least 12 characters; the default
username is `Ahmed` and can be changed with `ADMIN_USERNAME`. An optional
second full-access developer login can be configured with
`DEVELOPER_USERNAME` and `DEVELOPER_PASSWORD`. Admin sessions expire after
12 hours. On the first successful admin sign-in, the configured username and
password are bootstrapped into PostgreSQL; subsequent admin username and
password changes are stored there. The admin email is fixed to
`ahmed.chouikh2020@gmail.com`.

The console retains request method, path, status, duration, timestamp, direct
peer IP, bounded user-agent, and request/response byte counts for 30 days.
Request bodies, query-string values, cookies, authorization headers,
passwords, and tokens are never recorded. Daily usage and sampled server
resource aggregates are retained for long-range comparisons. Optional user
age, gender, and country are used for aggregate charts only. Error reports
support up to three private PNG, JPEG, or WebP screenshots (5 MiB each);
attachments are stored in PostgreSQL and removed with their report/account.
Authorized staff can inspect user library contents, so protect admin
credentials and the console deployment.
The dashboard is a static Next.js export embedded in the Go binary. After
changing the dashboard, rebuild it before building or testing the API:

```powershell
cd internal/api/usage_next
pnpm install --frozen-lockfile
pnpm build
```

The console includes account suspension and password resets, an editable admin
profile, detailed request views, demographic and long-range usage analytics,
user-submitted error reports with private screenshots, product insights, and
book publishing with optional cover and reading-file uploads. Banned users and
sessions invalidated by an administrator are blocked by the API.

Email notifications are disabled by default. Verified users can opt in to
actionable sync-conflict and account-security emails from Settings.

All data and file routes require `Authorization: Bearer <token>`. Sign-in
returns a 24-hour HMAC JWT by default, or a 30-day JWT when “Remember me” is
selected. Users can change the active session length from Profile after
confirming their password. The Web frontend and
desktop app should use the same API origin (`VITE_API_BASE_URL`); set
`CORS_ORIGINS` to include the Web app's origin. API errors use
`{"error":{"code":"...","message":"..."}}`.

Routes:

- `POST /api/auth/signup`, `POST /api/auth/signin`, `GET /api/auth/me`
- `POST /api/auth/verify-email`, `POST /api/auth/verification/resend`
- `POST /api/auth/password/forgot`, `POST /api/auth/password/reset`
- `GET|PATCH /api/auth/email-notifications`, `POST /api/auth/password/verify`,
  `PATCH /api/auth/password`
- `PATCH /api/auth/profile` (password-confirmed name and optional demographics)
- `PATCH /api/auth/session` (password-confirmed session duration update; returns a new token)
- `GET /api/notifications?limit=100&offset=0` (paginated account notifications),
  `POST /api/notifications/{id}/read`, and authenticated
  `DELETE /api/notifications[/{id}]` for clearing one or all account notifications
- `/api/admin/*` (console login, health/charts, request history, users, error
  reports, user insights, notifications, and public-book management; requires
  an admin/developer session)
- `GET /api/admin/analytics?period=day|month|year` (historical request and
  sampled server metrics with a previous-period comparison)
- `GET|POST /api/admin/notes`, `PATCH|DELETE /api/admin/notes/{id}`
  (private admin notes; writes require password reauthentication)
- `GET|PATCH /api/admin/profile`, `PATCH /api/admin/profile/password`
  (primary admin profile; mutations require password reauthentication)
- `POST /api/reports` (signed-in product feedback)
- `GET /api/public/books`, `POST /api/public/books/{id}/add`
- `POST /api/auth/account/delete` (requires the account password; permanently deletes the account and cloud data)
- `GET|POST /api/books`, `GET|PATCH|DELETE /api/books/{id}`
- `GET|POST /api/books/{id}/notes`, CRUD `/api/notes`
- `GET|PATCH /api/progress/{bookId}`
- CRUD collections: `/api/writings`, `/api/reading-plans`,
  `/api/reading-sessions` (updates/deletes use `/{id}`)
- `POST /api/sync/merge` (also available as `/api/sync/import-json`),
  `GET /api/sync/pull`
- `POST /api/sync/replace` (requires the account password; atomically replaces all cloud records)
- `GET|POST /api/sync/conflicts`
- `POST /api/upload`, `GET /api/files/{type}/{filename}`
- `POST /api/ai/chat`, `POST /api/ai/writing-assist`
- `POST /api/error-reports` (authenticated; accepts a report title, message,
  details, reproduction steps, client metadata, and up to three bounded
  screenshots; JSON-only legacy submissions remain supported)

Sync JSON uses `books`, `writings`, `readingPlans`, `readingSessions`, `notes`,
and `progress` arrays. Imported UUID records are merged only when their
`updatedAt` is newer than the stored version. AI routes return an explicit
unavailable response until `ANTHROPIC_API_KEY` is configured.
