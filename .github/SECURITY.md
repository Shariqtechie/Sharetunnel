# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems.
Use GitHub's private reporting instead: **Security tab, Report a vulnerability**.
I aim to reply within 7 days and to publish a fix and an advisory once it is safe to do so.

## Supported versions

Only the latest release receives security fixes.

## What ShareTunnel can and cannot do

Understanding the trust model helps you deploy it safely:

- **Docker socket access.** ShareTunnel mounts `/var/run/docker.sock` to run `occ` inside your Nextcloud container and to replace itself during updates. Anything with this access can control Docker on the host. Keep the panel password strong and the panel off the public internet.
- **The panel is never tunnelled.** Only Nextcloud's port is exposed through the Cloudflare tunnel. The panel (default port 9099) stays on your LAN or VPN.
- **Database access is read-only.** Shares are listed with a read-only PostgreSQL session. Nothing is ever written to Nextcloud's database by ShareTunnel.
- **Panel hardening.** Password login with constant-time comparison, brute-force lockout, `HttpOnly` and `SameSite=Strict` session cookies, a strict Content-Security-Policy and no third-party requests.
- **Quick tunnel addresses are unguessable but public.** Anyone with a Nextcloud share link can open it. Use passwords and expiry dates on sensitive shares.
