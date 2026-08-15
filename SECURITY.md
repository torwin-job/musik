# Security policy

## Reporting a vulnerability

Do not open a public issue containing exploit details, credentials, private
library metadata or server addresses. Use GitHub Private Vulnerability
Reporting for this repository. If it is not enabled, open a minimal issue
asking the maintainer for a private contact channel.

Include:

- affected version or commit;
- deployment type (Docker, bare metal, reverse proxy);
- reproducible steps with all secrets redacted;
- expected impact and any suggested mitigation.

## Secrets

The repository must never contain:

- `.env` or `deploy/secrets.env`;
- `MUSIK_PASSWORD`, `MUSIK_API_TOKEN` or `MUSIK_SESSION_SECRET` values;
- SSH private keys, TLS private keys or production proxy configuration;
- database, artwork, embeddings, music-library or wishlist exports;
- APK/AAB files built with private `--dart-define` values.

Generate independent values for each deployment:

```bash
openssl rand -hex 24  # password
openssl rand -hex 32  # API token
openssl rand -hex 32  # session secret
```

Treat secrets embedded into an APK as publicly recoverable. If a secret is
committed, logged, shared in chat or embedded in a distributed binary, rotate
it immediately; deleting the file is not sufficient.

## Deployment baseline

- Keep authentication enabled on every public interface.
- Publish only the Go player port; keep the Python worker private.
- Terminate TLS at a maintained reverse proxy.
- Mount the music library read-only.
- Back up SQLite consistently, including WAL data when applicable.
- Keep container images, Flutter, Go, Python and ffmpeg updated.

Only the latest revision on the default branch receives security fixes.
