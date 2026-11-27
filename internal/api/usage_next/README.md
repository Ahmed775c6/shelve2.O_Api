# Shelve Admin Control Center

This Next.js dashboard is exported as static assets and served by the Shelve Go API at `/usage`. Its source is organized into dashboard pages, shared admin components, API helpers, and data types.

## Development

Install dependencies and run the dashboard:

```bash
pnpm install
pnpm dev
```

The local dashboard is available at `http://localhost:3000/usage`.

## Production

Build the static dashboard:

```bash
pnpm build
```

The build writes to `out/`, which is embedded into the Go API. Rebuild it after changing the dashboard so the API serves the updated assets.

## Dashboard capabilities

- System health, database size, request and user trends, platform and response-status charts.
- Country, age-range, and gender aggregates with count/share chart tooltips.
- Daily, monthly, and yearly traffic history with previous-period comparisons and sampled server metrics.
- User, error report, insight, public library, and notification management.
- Admin and user request history with IP, client platform, transfer sizes, actor filters, and per-request detail views.
- Persisted admin username, profile image, fixed email, and password settings.
- Detailed user error diagnostics and privately stored screenshot previews.
- Periodic admin alerts for new errors, insights, and recent server errors.
- Light, dark, and system themes.

The initial admin login uses the API's configured credentials and bootstraps the primary admin profile in PostgreSQL. Thereafter, the stored admin username and password are authoritative. Every admin `POST`, `PATCH`, and `DELETE` action requires the current admin password again. Request IPs show the direct connected peer; forwarded client IP headers are not trusted.
