# ShareTunnel

Share Nextcloud files with anyone through a temporary public link. No domain, no port forwarding.
ShareTunnel runs a Cloudflare quick tunnel, adds its URL to Nextcloud's trusted domains by itself,
and shows every public share link in a small web panel.

## Install

Set a real password in `docker-compose.yml`, then run it (or import it as a custom app in CasaOS):

    docker compose up -d

Open `http://<server-ip>:9099`. Keep that port on your LAN or Tailscale, never public.

## Updates

The panel checks GitHub Releases every 6 hours (and on **Check for updates**). Choose in the panel:
just tell me, update automatically at a chosen hour, or don't check. A failed update rolls back by itself.

## Publishing a release (maintainer)

Actions tab, **build-and-publish**, **Run workflow**, type a version like `0.1.1`.
It builds the image, tags it `:0.1.1` and `:latest`, then creates the GitHub Release.
Every push to `main` also builds `:edge` for testing.

## Layout

    main.go        commands and config
    manager.go     tunnel start/stop/restart, trusted_domains
    panel.go       web server and API
    updater.go     update check, apply, rollback
    settings.go    saved settings
    web/           index.html, css/style.css, js/app.js
