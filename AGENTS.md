# Repository Guidelines

## Project Structure & Module Organization

This Flask application manages Microsoft 365 tenants through MSAL authentication and Microsoft Graph. `app.py` contains routes, authentication, configuration persistence, token caching, and Graph helpers. `templates/` holds Jinja pages sharing `base.html`; `static/style.css` provides local styling. `sku_product_mapping.json` overrides built-in license names at startup. `screenshot/` contains documentation images. Dependencies are pinned in `requirements.txt`; `Dockerfile` uses Python 3.10 and Gunicorn. `tests/` contains isolated regression tests; `.github/workflows/docker.yml` builds amd64/arm64 GHCR images.

## Build, Test, and Development Commands

Run commands from the repository root:

- `python -m venv .venv`: create a local environment; on PowerShell, activate with `.\.venv\Scripts\Activate.ps1`.
- `python -m pip install -r requirements.txt`: install dependencies.
- `python -m flask --app app run --debug --port 5000`: start local development at `http://127.0.0.1:5000`.
- `docker build -t office365-manager .`: build the application image.
- `docker run --rm -p 5000:5000 office365-manager`: run the image using Gunicorn.

## Coding Style & Naming Conventions

Use four-space indentation in Python and CSS; preserve surrounding Jinja indentation. Use `snake_case` for Python functions, variables, and template filenames, and `UPPER_SNAKE_CASE` for constants. Follow existing annotations when changing Python functions. Keep interface text in Chinese and reuse shared templates and CSS classes. Route Graph calls through `graph_request` where applicable. No formatter or linter is configured. Keep changes focused; avoid unrelated refactoring and dependency changes.

## Testing Guidelines

Run `python -m unittest discover -s tests -v` for the standard-library regression suite. No coverage threshold is configured. Manually verify affected flows: login/logout, organization switching, user pagination, roles, and license assignment. Check Graph error responses as well as success cases. Use a dedicated test tenant for account mutations. For automated tests, use `tests/test_*.py`, Flask's test client, mocked MSAL/Graph calls, and isolated configuration. Importing `app.py` creates `config.json` when absent.

## Commit & Pull Request Guidelines

History uses short imperative subjects such as `Fix TypeError in /users endpoint when Graph API returns errors`; no conventional prefix is established. Describe the concrete change. PRs should explain the problem, affected flows, and validation performed, link relevant issues, and include screenshots for UI changes.

## Security & Configuration

Never commit `config.json`, tenant secrets, or tokens. Set `FLASK_SECRET_KEY` and change the initial login password before deployment. Compose persists `CONFIG_FILE=/app/data/config.json` through `./data`; plain Docker defaults to `/app/config.json`. Exclude credentials and personal tenant data from logs and screenshots.
