# Changelog

All notable changes to this project are documented here.
Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versioning follows [SemVer](https://semver.org/).

## [0.3.1] - 2026-10-25

### Added
- **Choose your password on first open.** `PANEL_PASSWORD` is now optional. Without it, a fresh install shows a "Create your password" page (minimum 8 characters). Only devices on your home network can create it; requests from the internet or through a proxy are refused. The password is stored salted and hashed in the data folder.
- **Forgot your password?** A wrong password now shows how to get back in: delete `auth.json` in the data folder and reload, or run `docker exec sharetunnel sharetunnel reset-password`, which prints a new random password. Settings and shared links are kept, and everyone is signed out.
- **Change password** on the Setup page. It asks for the current password and signs out every other browser.
- `UPDATE_MODE=managed` for app stores such as CasaOS: turns off the built-in updater, shows when a new version exists, and points to the store's Update button.

### Changed
- A fresh install no longer starts the tunnel by itself. Press **Start tunnel** once; after that your choice is remembered.
- `PANEL_PASSWORD=change-me` (the old example value) now counts as "not set", so you get the first-run page instead of a startup error.
- A password set with `PANEL_PASSWORD` always wins over a saved one.

## [0.3.0] - 2026-10-24

### Added
- **Setup page and first-run checks.** ShareTunnel now verifies Docker access, the Nextcloud container, that Nextcloud is installed, the Nextcloud address, the database and cloudflared, and explains any problem in plain words with a suggested fix.
- Detects Nextcloud and PostgreSQL containers on the server and lets you pick them (and the Nextcloud address) on the new **Setup** page. Changes apply immediately, no restart needed.
- Dashboard banner and menu badge when setup needs attention.
- `sharetunnel doctor` command (`docker exec sharetunnel sharetunnel doctor`) prints the same checks in the terminal.

### Changed
- The tunnel no longer starts blindly. If Docker or Nextcloud isn't ready (for example right after a reboot) it reports why and retries every 30 seconds until it is.
- Clear messages for a missing password, an invalid or already-used `PANEL_PORT`, and Docker or database errors instead of raw error text.
- Stopping the tunnel can no longer hang if the tunnel process misbehaves.

## [0.2.0] - 2026-10-23

### Added
- Completely redesigned admin panel: sidebar navigation, dashboard with live stats, system health checks, searchable and sortable shared links table, activity log with download, light and dark themes, and a mobile layout.
- Shared links now show type (folder, video, image, audio, file), size, owner, password protection, expiry and when they were shared.
- Demo mode (`DEMO=true`) to try the panel without Nextcloud, Docker access or a tunnel.
- Health check for the container, security headers (CSP, frame and sniffing protection) and a `Secure` session cookie behind HTTPS proxies.
- Multi-architecture images (amd64 and arm64, so Raspberry Pi works).

### Changed
- Release notes shown in the panel now come from this changelog.

## [0.1.1] - 2026-10-23

### Changed
- Version published to verify the in-app updater end to end.

## [0.1.0] - 2026-10-23

### Added
- Cloudflare quick tunnel to Nextcloud, with the temporary address added to Nextcloud's trusted domains automatically and removed on stop.
- Web panel on port 9099 with password login, start and stop, public address and shared links list.
- In-app updates from GitHub Releases: check now, update now, automatic mode, and rollback if the new version fails to start.
