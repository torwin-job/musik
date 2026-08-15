# Contributing

## Development setup

1. Copy `.env.example` to `.env` and use local, unique values.
2. Never disable authentication on a public interface.
3. Keep music, databases, caches, logs and generated binaries outside Git.

Run the relevant checks before submitting a pull request:

```bash
make test-python
make test-go
make test-flutter
```

For Go changes, run `gofmt`. For Flutter changes, run `dart format`,
`flutter analyze` and `flutter test`.

## Pull requests

- Keep changes focused and explain user-visible behavior.
- Add tests for bug fixes and API contract changes.
- Update `.env.example` and documentation for new configuration.
- Do not commit generated APKs, model caches or personal library examples.
- Use synthetic metadata in fixtures and screenshots.

## Secret check

Before staging:

```bash
git status --short --ignored
gitleaks detect --no-git --source .
```

If a secret is accidentally staged or committed, stop and rotate it before
continuing. Rewriting history does not revoke an exposed credential.
