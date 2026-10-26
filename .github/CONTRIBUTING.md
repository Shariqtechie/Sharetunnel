# Contributing

Thanks for helping make ShareTunnel better. Bug reports, ideas and pull requests are all welcome.

## Run it locally in 30 seconds

You need Go 1.22 or newer. No Nextcloud, Docker or tunnel needed, because demo mode fakes them:

```bash
DEMO=true DATA_DIR=/tmp/st-data go run ./cmd/sharetunnel
# open http://localhost:9099, password: demo
```

The UI lives in `web/` (plain HTML, CSS and JavaScript, no build step). Edit a file, restart, refresh.

## Before you open a pull request

```bash
gofmt -l .          # should print nothing
go vet ./...
go test ./...
node --check web/js/app.js
```

CI runs the same checks and also builds the Docker image for amd64 and arm64.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/sharetunnel/main.go` | Commands, configuration, Nextcloud and database helpers |
| `cmd/sharetunnel/manager.go` | Tunnel lifecycle: start, stop, auto-restart, trusted domains |
| `cmd/sharetunnel/panel.go` | Web server and API |
| `cmd/sharetunnel/auth.go` | Panel password: first-run setup, sign-in, reset |
| `cmd/sharetunnel/doctor.go` | Setup checks and plain-words error messages |
| `cmd/sharetunnel/updater.go` | Update check, apply and rollback |
| `cmd/sharetunnel/settings.go` | Saved settings |
| `cmd/sharetunnel/demo.go` | Demo mode data |
| `web/` | The admin panel UI (HTML, CSS, JavaScript) |
| `assets/`, `docs/` | Logo, banner and screenshots |
| `.github/` | CI and release workflows, issue templates, contributing and security notes |

## Guidelines

- Keep it small and dependency-free. The Go code uses only the standard library.
- Anything user-controlled (file names, owners) must be inserted with `textContent`, never as HTML.
- Add a line to `CHANGELOG.md` under the next version for user-visible changes. Release notes are generated from it.
- Be kind. Assume good intent.
