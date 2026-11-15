# Multi-user identity and profile contract

Status: integrated in `home`, schema version 15. Migrations create local users,
roles, verified OIDC identities, hashed invites, revocable browser sessions,
user-owned device tokens and an administrator-invariant lock row, then assign
legacy personal rows to one disabled installation owner and its default
profile. The first admin activates that owner through a one-time CLI invitation,
preserving existing profile data. Browser requests resolve an owned session
profile; device Bearer requests resolve a separately fixed, owned profile.
Both use profile-scoped player and worker queries. Browser state changes require
CSRF protection. OIDC discovery, PKCE, token verification, invitation-only
provisioning and explicit identity linking are wired to the UI. Shared password,
shared `MUSIK_API_TOKEN` and auth-disabled mode are rejected in multi-user mode;
personal device Bearer tokens are supported.

SQLite HTTP tests cover token lifecycle, exact legacy `auth/me`, profile
isolation, old events, sessions, Bearer stream/artwork, jobs and logout.
PostgreSQL 17 integration tests cover migrations, profile isolation, statistics
upserts and independent-connection administrator changes. Native playback and
background audio still require a physical device or emulator.

This contract separates a person who can sign in (`User`) from listening state (`Profile`). One user may own multiple independent profiles. Identity providers authenticate users; they do not own musik data or define profile semantics.

## Implemented boundary

- `player/internal/auth` verifies the configured OIDC provider and issues opaque, revocable server sessions. The authenticated principal carries user, role, and active-profile IDs; the profile ID comes from owned server-side session state.
- `player/internal/api` exposes profile management, account administration, invitations, OIDC login/link, and the existing playback/library APIs. Cookie-authenticated writes enforce CSRF/Origin checks.
- The schema includes users, roles, external identities, auth sessions, invitations, profiles, device tokens and profile ownership for personal rows. Catalog data and worker jobs remain installation-wide.
- Multi-user startup fails closed when OIDC or the initial administrator invitation is missing. `MUSIK_PASSWORD`, shared `MUSIK_API_TOKEN`, and `MUSIK_AUTH_DISABLED` are rejected in this mode.
- Device tokens belong to a user and reference one owned profile. Only their SHA-256 hashes are stored; the secret is returned at creation/replacement and expires after 180 days. Deleting the selected profile or disabling its owner invalidates tokens; re-enabling an account does not restore them.
- Browser-only routes manage device tokens with cookie-session and CSRF/Origin checks. A device token cannot manage tokens/accounts/profiles/shared-library jobs and can enqueue only its profile's `mix_pack` job.
- Go and Python repositories use profile-scoped stores. Every personal-data query binds `profile_id` explicitly (sqlx in Go, SQLAlchemy in Python); no adapter injects or rewrites it.

## Domain contract

### User and identity

- `UserID` is an application-generated, opaque UUID represented as text. It is stable across providers and database engines.
- A user has status `active | disabled`, a display name, timestamps, and local musik roles. Roles are authorization policy, not identity-provider claims copied blindly.
- `ExternalIdentity` is keyed by `(issuer, subject)` from a verified OpenID Connect ID token and references exactly one `UserID`. Enforce uniqueness on `(issuer, subject)`.
- Email, username, and display name are mutable attributes. They are never identity keys and are never used to silently merge accounts.
- Linking another OIDC identity to an existing user requires an authenticated, explicit linking flow. First-login provisioning is invite-gated: an OIDC callback can create a user only after consuming a valid, single-use invitation. Open JIT registration is out of scope.
- Invitations have opaque, high-entropy, single-use secrets stored only as hashes, an expiry, optional verified-email binding, issuer/admin audit fields, and revocation. They grant only the ordinary `user` role. Email is an optional invitation constraint, never the account identity key.
- Do not trust user IDs, roles, email, or profile IDs supplied in request bodies or proxy headers. The authenticated principal is constructed by server middleware.

### Profile

- `ProfileID` is an application-generated UUID. Every profile has exactly one owning `UserID`; every user has at least one active profile and exactly one default profile.
- A profile contains independent taste, listening history, recommendation impressions, radio preferences/rules, contexts, favorites, listen-later items, user playlists and playback sessions.
- A user may have several profiles (for example, personal, family-shared listening or a child's profile). Limits and public sharing of a profile are deferred policies, not implicit behavior.
- The current active profile is session state controlled by the server. Activation must check `profile.owner_user_id == principal.user_id`. Foreign and nonexistent profile IDs return the same not-found response.
- Profile-scoped caches and in-memory models use `(ProfileID, model/version)` keys. A profile switch never reuses another profile's taste or queue state.
- Deleting a user/profile is initially soft deletion. Hard deletion and retention/export policy require a separate data lifecycle decision.

### Data ownership

| Shared installation data | User-owned data | Profile-owned data |
|---|---|---|
| scanned tracks and paths, audio features/embeddings, artwork, artist/album catalog, scanner state, shared worker jobs | login identities, local roles, account settings | event history, recommendation requests/impressions, taste snapshots/vectors, favorites, later items, rules, contexts, playlists, play sessions, per-profile recommendation preferences |

Recommendation feedback and listening events must never update another profile's state. Installation-wide metrics, if retained, are explicitly aggregate and must not be used as a substitute for profile ownership. Public radio shares remain bearer-token resources; playback by a share listener is not owner feedback.

## Authentication contract

- Keep an authentication-provider boundary: OIDC produces the same internal `Principal` consumed by handlers and domain services; those layers do not depend on Keycloak. The existing local password provider remains available only in legacy single-user mode.
- OIDC uses Authorization Code + PKCE, verified issuer/audience/signature/expiry/nonce, discovery/JWKS, one-time state, and an allowlisted redirect URI. Keycloak is supported through standard OIDC configuration; there is no Keycloak-specific data model.
- Browser tokens stay server-side. Successful login creates a rotated, opaque, `HttpOnly`, `Secure` (when HTTPS), `SameSite=Lax` cookie backed by revocable server session state. State-changing cookie-authenticated requests also require CSRF/Origin protection.
- Existing `MUSIK_PASSWORD` and the shared `MUSIK_API_TOKEN` remain single-user compatibility settings only. Multi-user mode rejects them; user-owned device Bearer tokens preserve the installed Flutter client's existing protocol.
- Multi-user mode fails closed if OIDC or the initial administrator bootstrap is missing. A bootstrap CLI creates the first administrator invitation; subsequent invitations, provider linking, role assignment and account disabling are admin-controlled operations. Do not rely on Keycloak group names for authorization unless an explicit, documented mapping is configured.

## HTTP/API contract (v1)

Existing library/playback URLs remain stable. They resolve the active profile from the authenticated server session; clients cannot select a data owner by adding `user_id` or `profile_id` to ordinary payloads.

- Browser `GET /api/auth/me` returns `{ "authenticated": true, "user": {"id", "display_name", "roles"}, "active_profile": {"id", "name"}, "profiles": [...] }` (or the existing unauthenticated result). A device Bearer deliberately gets the legacy mobile shape `{ "ok": true, "auth_enabled": true }`; invalid Bearer gets HTTP 200 with `ok:false`. Other protected APIs return 401 without an OIDC HTML redirect.
- `GET /api/profiles` lists only the caller's profiles.
- `POST /api/profiles` accepts `{ "name": "..." }` and returns the created profile. The service creates the owner relation from the principal.
- `PATCH /api/profiles/{profile_id}` changes that profile's name/settings only after ownership validation.
- `POST /api/profiles/{profile_id}/activate` changes the current authenticated session's active profile and returns the selected profile. It does not accept an owner ID.
- `DELETE /api/profiles/{profile_id}` soft-deletes a non-last profile owned by the caller; deleting the last profile is rejected. Default-profile reassignment is transactional.
- `POST /api/admin/invitations` creates a single-use, expiring invitation and returns its secret once; `GET` and `DELETE /api/admin/invitations/{id}` list/revoke invitations. Delivery is out of scope initially; the administrator transmits the secret through their chosen channel. Responses never reveal whether an arbitrary email already has an account.
- `GET /api/admin/users` and `PATCH /api/admin/users/{id}` let administrators review accounts, disable sessions and assign the local admin role while preserving at least one active administrator.
- `GET/POST /api/account/device-tokens`, `DELETE /api/account/device-tokens/{id}` and `POST /api/account/device-tokens/{id}/replace` manage the browser user's devices. Replacement is atomic; lists return metadata only, never secrets or hashes.
- `POST /api/auth/oidc/default/link` starts an explicit, CSRF-protected flow from an authenticated session. The callback binds the verified identity to that same live session; email is not used to merge identities.
- OIDC callback consumes an invitation atomically before provisioning a user. A failed or expired invitation creates no account or session.
- OIDC uses `/api/auth/oidc/default/start` and `/api/auth/oidc/default/callback`. Tokens and authorization codes are never returned to browser JavaScript.
- API error envelope stays `{ "error": string, "code": string }`; use stable codes `auth_required`, `profile_not_found`, `profile_limit`, `profile_required`, and `provider_unavailable`.

Every protected endpoint derives `UserID` and active `ProfileID` from request context. Repository methods that read or mutate personal state require a `ProfileID`; access checks and mutations happen in the same transaction where a race could change ownership.

## Persistence and migration contract

- Add normalized `users`, `external_identities`, `auth_sessions`, `profiles`, `invitations`, `device_tokens`, and installation state records. User/profile/invitation/token IDs are text UUIDs on SQLite and PostgreSQL alike; invitation and device secrets are stored as hashes. Enforce ownership with foreign keys and indexes; enforce one default profile per user with an equivalent constraint/transaction rule on both engines.
- Add `profile_id` to profile-owned rows and scope every query/update/delete by it. Shared catalog rows stay unowned. Composite ownership constraints should prevent playlist items, contexts and active sessions from referencing a foreign profile's parent.
- A versioned migration creates one pending bootstrap owner and one default profile, then assigns all existing personal rows to that profile exactly once. It also creates the `active_admin_guard` row that serializes role removal and account disabling across targets. The initial administrator completes OIDC through the CLI-created bootstrap invitation before multi-user mode is enabled. It never copies legacy history or taste to each invited account. The old single-user login remains available only until cutover; multi-user mode rejects its shared password and token.
- Each step is restartable or has a defined rollback/backup path. Migration refuses ambiguous existing ownership rather than guessing.

## Acceptance contract

1. Two users cannot list, infer, mutate or delete each other's profiles or profile data, including by changing IDs in URLs, query strings, headers or JSON bodies.
2. One user can create and switch between two profiles; favorites/history/taste/playlists and active playback state remain isolated across switches and devices.
3. Duplicate `(issuer, subject)` login resolves to the same user; same email with a different subject does not auto-link.
4. Invalid issuer, audience, signature, nonce, state, expiry, redirect URI, disabled user and revoked session all fail closed.
5. Legacy single-user migration preserves every existing row under one owner/default profile, with counts and representative taste/playback behavior reconciled before and after.
6. Share-radio playback remains read-only and never writes feedback to the share owner's profile.
7. Run repository and service contracts against both SQLite and PostgreSQL fixtures; do not infer PostgreSQL behavior from SQLite-only tests.

## Decisions and remaining implementation policy

- Resolved: users enter through OIDC only after accepting a valid invite; open JIT registration is disabled.
- Resolved: `MUSIK_PASSWORD` and shared `MUSIK_API_TOKEN` do not work in multi-user mode; they remain compatibility options only for single-user deployments. Personal profile-bound device Bearer tokens preserve the existing Flutter client protocol.
- Resolved: the first PostgreSQL release includes a verified SQLite-to-PostgreSQL data transfer; the import is offline and keeps the SQLite source intact.
- Initial administrator bootstrap uses a one-time CLI invitation.
- Resolved: there is no profile-count limit in the first release; deleting a user's last active profile is rejected.
