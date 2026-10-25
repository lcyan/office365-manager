# Repository Guidelines

## Project Structure & Module Organization

This is a Go web application (module `github.com/lcyan/office365-manager`) that manages Microsoft 365 tenants through the Microsoft Graph API (client-credentials flow). `cmd/server/main.go` wires dependencies and starts the HTTP server. `internal/config` loads, validates, and atomically persists `config.json` (organizations + login password hash, with automatic migration of the legacy Python-version format). `internal/auth` provides argon2id password hashing (with transparent upgrade of legacy SHA-256 hashes) and login rate limiting. `internal/graph` wraps the Graph API: token cache per organization, unified OData escaping, GUID validation for object IDs, typed errors, count-based pagination, and nextLink following. `internal/web` contains sessions (HMAC-signed cookie), middleware (auth gate, CSRF, security headers), the template renderer, and all page handlers. `internal/background` runs retrying background tasks with structured logs. `internal/licenses` maps SKU part numbers to product names. `assets/` holds templates, static CSS (shadcn-style design tokens), and the SKU mapping, embedded via `go:embed` into a single binary.

## Build, Test, and Development Commands

Run from the repository root:

- `go vet ./...`: static analysis.
- `go test ./...`: full test suite (all Graph calls are faked via test servers; tests never touch real tenants).
- `DEV_MODE=1 go run ./cmd/server`: start locally on `:5000` (`SESSION_SECRET` optional in dev mode; required in production).
- `go build -o office365-manager ./cmd/server`: build the binary.
- `docker build -t office365-manager .`: multi-stage build (distroless static, ~20 MB).

## Coding Style & Naming Conventions

Standard Go formatting (`gofmt`) and idioms. Follow effective Go: exported identifiers documented with doc comments (Chinese is fine for this repo), errors wrapped with context via `fmt.Errorf("...: %w", err)`. Package-level state must be mutex-protected. Handler flow: build explicit view models (no global template injection), then render via `renderPage` or redirect via `redirect` so the session is always saved. Route all Graph access through `internal/graph` client methods — never build `$filter` strings by hand; use `graph.Quote` for OData literals and `graph.ValidID` before interpolating IDs into paths. UI text is Chinese; keep the shadcn-style CSS class vocabulary (`btn`, `card`, `badge`, `alert`, `table`, …) instead of introducing new utility classes ad hoc. Config writes only via `config.Store` methods (atomic).

## Testing Guidelines

`go test ./...` must stay green; CI additionally runs `-race`. Write table-driven unit tests inside each package and HTTP-level tests in `internal/web` using the fake Graph backend helpers (`newTestServerOpts`, `graphHandler`). Cover Graph error responses (4xx/5xx and network failures) as well as success cases. Regression tests exist for the Python-version bugs fixed by this rewrite (delete-user ordering, silent license failures, group principals in role filters, pagination consistency, CSRF, open redirect, OData injection) — keep them passing and extend the pattern for new fixes. Never make network calls from tests.

## Commit & Pull Request Guidelines

History uses short imperative subjects such as `Add user summary, count-based pagination, role filter and detail views`. Describe the concrete change. PRs should explain the problem, affected flows, and validation performed (`go test ./...` output), and include screenshots for UI changes.

## Security & Configuration

Never commit `config.json`, `.env`, `data/`, tenant secrets, or tokens. `SESSION_SECRET` must be set in production (the server refuses to start otherwise). Default login password is `admin` — the dashboard warns until changed. Destructive actions must remain POST + confirmation pages behind CSRF. Logs must not contain tokens, secrets, or full response bodies — record resource category, method, status, and duration only.
