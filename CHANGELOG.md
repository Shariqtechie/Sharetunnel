# Changelog

All notable changes to this project are documented here.
Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versioning follows [SemVer](https://semver.org/).

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
