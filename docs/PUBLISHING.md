# Publishing safely

The backend repository and Flutter application are intended to be published
as separate Git repositories.

## Backend repository

From the project root:

```bash
git init
git branch -M main
git status --short --ignored
gitleaks detect --no-git --source .
git add .
git status --short
```

Before committing, confirm that the staged list does not contain:

- `.env`, `deploy/secrets.env`, `deploy/.ssh/`;
- `data/`, wishlist exports or music metadata;
- APK/AAB files, Go binaries, logs or model caches;
- site-specific nginx configuration.

Then:

```bash
git commit -m "Initial public release"
git remote add origin git@github.com:OWNER/REPOSITORY.git
git push -u origin main
```

## Flutter repository

The root `.gitignore` excludes `mobile/flutter/`. Initialize it independently:

```bash
cd mobile/flutter
git init
git branch -M main
git status --short --ignored
gitleaks detect --no-git --source .
git add .
git status --short
```

Do not publish APKs built with private `--dart-define` values. GitHub Actions
for the mobile repository runs formatting, analysis, tests and Gitleaks.

## GitHub settings

For both repositories:

1. Enable Private Vulnerability Reporting.
2. Enable secret scanning and push protection where available.
3. Protect `main`: require pull requests and passing CI.
4. Disable Actions write permissions unless a workflow needs them.
5. Do not add production credentials as repository variables. Use encrypted
   Actions secrets only for workflows that genuinely require them.

## If a credential was exposed

Removing a value from source or rewriting Git history does not make it safe.
Rotate the server password, API token, session secret and affected SSH keys,
then invalidate old sessions and rebuild private clients with the new values.
