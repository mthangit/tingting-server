# Ting backend design

> Status: steps 1–4 of the [plan](#11-plan) are built and running (bootstrap, Google sign-in, sync, money source
> catalog with logos on R2). Items marked **(planned)** are not built yet.
>
> Principle: **architecture, API and data are designed for the full product (many users); features start small.**
> There is one user today, but adding users or sign-in methods later does not change the API, data or handlers.

## 1. Goals

The app knows **one address**: `GET /v1/bootstrap`. It fetches the config from there and keeps a copy on the device.
Everything else comes from that config: the API list, feature flags, the money source catalog and logo URLs.
Changing an API, turning a feature on, or adding a bank is a server-side config change, not a new app release.

### Functional requirements

| # | Requirement | Status |
|---|---|---|
| B1 | The app fetches its config from one bootstrap API. The config is versioned and the app only downloads it again when it changes | Built |
| B2 | The config holds an **API catalog** (name → method + path). The app calls APIs by name, never by hardcoded URL | Built |
| B3 | The config holds feature flags, the minimum app version and sync timing | Built (the app does not enforce `minVersion` yet) |
| B4 | The config holds shared data: default money sources with logo URLs; later default tags and parser keywords | Sources built; tags and parser planned |
| B5 | Transactions, tags and money sources sync between device and server (push and pull), and work offline | Built |
| B6 | Sign-in, so data belongs to a user and works across devices | Built (Google) |
| B7 | With no network or a failing server, the app keeps working as before (local-first) | Built |

### Non-functional requirements

| | Target | Notes |
|---|---|---|
| Scale | Hundreds to a few thousand users | Personal app. One small server is enough |
| Latency | Bootstrap < 200 ms (p95), sync < 500 ms | Most bootstrap calls return `304 Not Modified` |
| Availability | 99.5% is enough | Local-first: if the server is down, sync is late but no data is lost |
| Security | Per-user data, HTTPS | Bank notification text is sensitive (see section 8) |
| Cost | As low as possible | One Cloud Run service, one Neon Postgres, logos on R2. All within free tiers today |

### Assumptions

- One developer, no existing infrastructure.
- No realtime between devices. Syncing on app open, after edits (debounced) and periodically while open is enough.
- Conflicts are rare (usually one person, one device), so last-write-wins is acceptable.

## 2. Overview

```
                ┌──────────────────────────────── iOS app ───────────────────────────────┐
                │  ConfigStore ──► ApiClient ──► SyncCoordinator ──► SyncEngine           │
                │  (active config,  (API by name,   (when to sync)    (push / pull)       │
                │   saved on disk)   tokens)                              │               │
                │       │                                                 ▼               │
                │       └──► SourceCatalogInstaller ──────────► SpendService / SpendStore │
                │            SourceLogo + LogoCache (memory only)                         │
                └─────┬──────────────────────────────┬──────────────────────┬────────────┘
                      │ 1. GET /v1/bootstrap          │ 2. other APIs, from   │ 3. logo images
                      │    (the only hardcoded URL)   │    the config catalog │    (logoUrl)
                      ▼                               ▼                       ▼
                ┌───────────────── ting-backend (Cloud Run) ──────────┐   ┌──────────────────┐
                │  Bootstrap    Auth    Sync                          │   │ Cloudflare R2    │
                └──────┬──────────────────┬───────────────────────────┘   │ bucket ting-logos│
                       │                  │                               └──────────────────┘
                ┌──────▼──────────────────▼──────┐
                │ Postgres (Neon)                 │
                │ configs, users, tokens, data    │
                └─────────────────────────────────┘
```

### App launch

```
App opens
 │
 ├─► Use the config saved on the device (or a built-in fallback with no APIs) → the app works without waiting
 │
 ├─► GET /v1/bootstrap  (If-None-Match: <current etag>), only after ttlSeconds have passed
 │      ├─ 304 → keep the current config
 │      ├─ 200 → validate → save → use it; install new catalog sources if catalog.sources.version changed
 │      └─ network error / bad config → keep the current config, try again next time
 │
 ├─► (planned) appVersion < minVersion → show an "Update required" screen
 │
 └─► "sync" feature on and signed in → SyncEngine runs (push, then pull)
```

## 3. Bootstrap API

### `GET /v1/bootstrap`

No sign-in needed. Request headers:

| Header | Example | Purpose |
|---|---|---|
| `X-App-Version` | `0.2.0` | Lets the server pick a config for the app version (planned) |
| `X-Platform` | `ios` | A different config for web or a mini app later |
| `X-Device-Id` | UUID created on first launch | Percentage rollouts (planned) |
| `If-None-Match` | `"cfg-42-…"` | `304` when the config hasn't changed |

### Response `200`

```json
{
  "schemaVersion": 1,
  "configVersion": 42,
  "ttlSeconds": 21600,
  "app": { "minVersion": "0.1.0", "latestVersion": "0.1.0" },
  "api": {
    "baseUrl": "https://<your-service>.run.app",
    "endpoints": {
      "auth.google":  { "method": "POST", "path": "/v1/auth/google" },
      "auth.refresh": { "method": "POST", "path": "/v1/auth/refresh" },
      "auth.logout":  { "method": "POST", "path": "/v1/auth/logout" },
      "sync.push":    { "method": "POST", "path": "/v1/sync/push", "timeoutMs": 20000 },
      "sync.pull":    { "method": "GET",  "path": "/v1/sync/pull" }
    }
  },
  "features": { "sync": true, "signInWithGoogle": true, "csvExport": false, "syncRawNotificationText": false },
  "sync": { "intervalSeconds": 900, "debounceSeconds": 5, "pushBatchSize": 200, "pullPageSize": 500 },
  "catalog": {
    "sources": { "version": 1, "items": [
      { "id": "6E0C7A90-0000-4000-8000-000000000140", "name": "Vietcombank", "kind": "bank", "sortOrder": 0,
        "logoUrl": "https://pub-<bucket-id>.r2.dev/logos/vietcombank.png",
        "aliases": ["vietcombank", "vcb"] }
    ] }
  }
}
```

Response headers: `ETag` (from the version and content), `Cache-Control: private, no-cache`.

Planned additions: `catalog.tags` (new default tags) and `parser` (extra keywords for `NotificationParser`, added to the
built-in set, never replacing it).

### How the API catalog is used

- The app calls APIs by **name** (`api.send("sync.push", …)`). `ConfigStore.endpoint(name)` joins `baseUrl + path`.
- A name missing from the config means the **feature is off**. For example, without `auth.google` the app hides the
  sign-in button; without `sync.push`/`sync.pull` it doesn't sync.
- Moving a path (`/v1/sync/push` → `/v2/sync/push`) or changing the domain is a config change.

### Config safety rules

A bad config can break the app on every device, so:

1. **Always a fallback.** Until a config has been downloaded, the app uses a built-in one with no APIs: everything
   networked is off and the app runs offline.
2. **Validate before use.** The server (`tingctl validate`, `config.Validate`) and the app both check. The app only
   accepts a config whose `api.baseUrl` is https on the same host as the bundled bootstrap URL (http only for
   localhost), and whose endpoint paths are relative.
3. **Keep the last good one.** The saved config is only replaced by a new valid one.
4. **Backward compatible.** The app ignores unknown fields, and a malformed catalog entry is skipped on its own
   without rejecting the config. Breaking changes bump `schemaVersion`.
5. **No executable logic.** Only data (keywords, URLs, flags), no arbitrary regex or scripts.
6. **Logos only from known hosts.** `logoUrl` must be https on `TING_ALLOWED_LOGO_HOSTS` on the server and on
   `TingLogoHosts` (Info.plist) in the app. Both default to `*.r2.dev`.

### How the app applies each part

| Config part | Where it's used in the app |
|---|---|
| `app.minVersion` | (planned) "Update required" screen |
| `api.*` | `ConfigStore` → `ApiClient` |
| `features.*` | Sign-in button, sync on/off, whether raw notification text is synced |
| `sync.*` | `SyncCoordinator` timing and batch sizes |
| `catalog.sources` | `SourceCatalogInstaller` adds **new** default sources (fixed IDs; never touches sources the user edited or deleted). `SourceLogo` loads the logo from `logoUrl` |

## 4. Sync API

Pulls follow a **server-issued cursor** (`rev`) rather than device time, so a wrong device clock can't lose changes.

### `POST /v1/sync/push`

```json
{
  "changes": {
    "transactions": [ { "id": "…", "amount": 89000, "updatedAt": "2026-10-24T05:10:00Z", "deletedAt": null, "…": "…" } ],
    "tags":         [ ],
    "sources":      [ ]
  }
}
```

Per record, the server:
- inserts it if it doesn't exist;
- overwrites it if the pushed `updatedAt` is **newer**; ignores it if older or equal, so resending is safe;
- bumps a per-user `rev` counter on every write.

Response:

```json
{ "accepted": 12, "ignored": 1, "rejected": [ { "type": "transaction", "id": "…", "reason": "…" } ] }
```

### `GET /v1/sync/pull?cursor=<rev>&limit=500`

```json
{
  "changes": { "transactions": [ ], "tags": [ ], "sources": [ ] },
  "nextCursor": 1532,
  "hasMore": false
}
```

Returns every record with `rev > cursor`, soft-deleted ones included. The app overwrites its copy when the server's
`updatedAt` is newer, saves `nextCursor`, and calls again while `hasMore` is true.

### On the app

```
SyncEngine.run():
  1. push every record with updatedAt > lastPushedAt, in batches of pushBatchSize
     (tags and sources first, then transactions; rawText/rawTitle stripped unless syncRawNotificationText)
  2. pull from the saved cursor until hasMore is false
  3. AppModel.reload() if anything was applied
A failure keeps the previous state; the next run resends safely. Data stays on the device.
```

`SyncCoordinator` runs it right after sign-in, when the app becomes active (then every `sync.intervalSeconds` while
it stays open), `sync.debounceSeconds` after each edit, and from the Sync row in Settings. It doesn't run in the
background. Progress is saved per user; signing out forgets it, so the next sign-in pushes everything again.

## 5. Sign-in

Handlers don't know how a user signed in: an **auth middleware** reads `Authorization` and puts the `userID` in the
request context. Changing sign-in methods only touches the middleware and the identities table.

**Phase 1 (built): Google Sign-In**
- Works with a free Apple Developer account (Sign in with Apple needs a paid one).
- The app gets an `idToken` from the GoogleSignIn SDK and calls `POST /v1/auth/google`.
- The server verifies the `idToken` with Google's public keys (`aud` is the app's OAuth client ID, `iss`, expiry),
  then finds or creates the user through `user_identities` with `provider = 'google'` and `subject` = Google's `sub`.
- It returns an `accessToken` (server-signed JWT, 15 minutes) and a `refreshToken` (30 days, kept in the Keychain,
  rotated on every refresh).
- The app shows "Sign in with Google" only when the config has `auth.google` and `signInWithGoogle` is on.

**Phase 2 (planned, needs a paid Apple Developer account): Sign in with Apple**
- Add `POST /v1/auth/apple` (Apple `identityToken`), verified with Apple's public keys, same `user_identities` table
  with `provider = 'apple'`. Token issuing, handlers and data stay the same.
- One user can link both Google and Apple.
- `DELETE /v1/account` to delete all server data (required for the App Store).

## 6. Server data

```
configs          (version PK, schema_version, payload JSONB, note, created_at, is_active)
                 -- one active row (unique partial index); versions are never edited

users            (id, created_at, deleted_at)
user_identities  (provider, subject, user_id, email, created_at)    PK (provider, subject)
devices          (user_id, id, platform, app_version, created_at, last_seen_at)
refresh_tokens   (token_hash, user_id, device_id, created_at, expires_at, revoked_at)
                 -- only SHA-256 is stored; each refresh revokes the old token; reusing a revoked one
                 -- revokes every token of that device

user_rev         (user_id PK, rev)                                  -- per-user increasing counter
transactions     (user_id, id, amount, kind, note, tag_id, occurred_at, source, origin,
                  raw_title, raw_text, balance_after, needs_review, excluded_from_totals,
                  created_at, updated_at, deleted_at, rev)          PK (user_id, id)
tags             (user_id, id, name, kind, color, sort_order, is_hidden,
                  created_at, updated_at, deleted_at, rev)          PK (user_id, id)
payment_sources  (user_id, id, name, kind, sort_order, is_hidden,
                  created_at, updated_at, deleted_at, rev)          PK (user_id, id)
```

- `(user_id, rev)` indexes on the three data tables keep pulls fast.
- Field names match `SpendJSON` (camelCase in the API, snake_case in the database).
- (planned) `config_rules` for per-platform, per-version configs and percentage rollouts.

## 7. Caching

| What | Where | How |
|---|---|---|
| Config | Server: in process, 30 s TTL | Bootstrap doesn't hit the database on every call |
| Config | App: JSON file in Application Support | ETag + `ttlSeconds`; the app only asks again after the TTL |
| Logos | R2 / Cloudflare CDN (`Cache-Control: public, max-age=604800`) and app memory | The app never writes logos to disk; offline it shows the bundled logos |
| User data | Not cached on the server | Small data, Postgres is fast enough |

## 8. Security and privacy

- Every data query filters by the `user_id` from the token, never from the request body.
- `rawText`/`rawTitle` (bank notification text, which can include balances and partial account numbers) are **not
  pushed by default**. The `features.syncRawNotificationText` flag turns that on; there's no per-user setting yet.
- HTTPS only. The app only talks to its bootstrap host and to logo hosts it was built with.
- Per-IP rate limits: bootstrap 30/min, auth 10/min, other APIs 120/min.
- Logs never contain request bodies or query strings: only method, path, status, duration, app version, platform
  and client IP.
- Deploys use Workload Identity Federation from GitHub Actions; there are no long-lived Google Cloud keys
  (see the README).

## 9. Operations

- **Hosting:** a Go container on **Google Cloud Run** (`ting-backend`, `us-central1`, scales to zero), Postgres on
  **Neon** (free tier, US), logos on **Cloudflare R2**. All free at current usage: Cloud Run's free tier is 2 million
  requests and 180,000 vCPU-seconds a month, and one user makes roughly 750 requests a month.
- **Releases:** push a `vX.Y.Z` tag to build the image, then run the **deploy** workflow with that version
  (see the README). Config changes are published separately with `tingctl`.
- **Scaling:** the service is stateless, so Cloud Run adds instances under load (capped at 2 today). Upgrade Neon
  when data grows.
- **Monitoring (planned):** error rate and p95 of bootstrap and sync, number of `rejected` records, apps per
  `configVersion`; alert on 5xx > 2% over 5 minutes.

## 10. Trade-offs and decisions

### Main trade-offs

| Decision | Gain | Cost |
|---|---|---|
| The app reads its API catalog from the config | Change APIs and turn features off without an app release | One more layer; a bad config affects every device (mitigated by section 3) |
| Last-write-wins per record | Simple, easy to get right | Two devices editing the same transaction at once lose one edit. Fine for a personal app |
| Server-issued `rev` cursor | Independent of device clocks | Needs a per-user counter (one small table) |
| One service | Cheap, easy to deploy and debug | Split later if needed |
| Raw notification text stays on the device | More private | The original notification isn't visible on other devices |
| Logos on R2, loaded straight by the app | Add banks without an app release; no image traffic on Cloud Run | Logos are downloaded again on each launch (memory cache only); offline shows bundled logos |

### Decided

| | Decision | Why |
|---|---|---|
| Language | **Go** | Single binary, fast cold starts (good for scale-to-zero), low memory |
| Database | **PostgreSQL on Neon** (free) | Relational data, transactions for `rev`, JSONB for configs; Neon sleeps and wakes on its own |
| Hosting | **Google Cloud Run** (free tier) | Scales to zero and out on demand. Needs a billing account but costs nothing within quota |
| Sign-in | **Google Sign-In first**, Apple later | Works with a free Apple Developer account; the auth layer is separate, so adding Apple changes nothing else |
| Config | `configs` table in Postgres | History, rollback by re-activating an old version |
| Logos | **Cloudflare R2** public bucket | Free egress, simple uploads with `wrangler`, no logo serving on the API |
| CI/CD | **GitHub Actions** → Artifact Registry → Cloud Run, Workload Identity Federation | No keys to store; tags build, a manual run deploys |
| Raw notification text | **Not** pushed by default | Privacy |

Firebase or Supabase as a full stack were not chosen because they don't run Go and impose their own patterns;
Supabase could still replace Neon as the Postgres host.

## 11. Plan

| Step | Work | Result | Status |
|---|---|---|---|
| 0 | `ConfigStore` and a built-in fallback config in the app | The app runs with no server | Done |
| 1 | `GET /v1/bootstrap` + `configs` table, deploy on Cloud Run + Neon | The app gets its config from the server, with ETag/304 | Done |
| 2 | Google Sign-In: `auth/google`, `auth/refresh`, JWT middleware, users, identities, refresh tokens; sign-in button | Accounts; the server knows whose data it is | Done |
| 3 | `sync/push`, `sync/pull` + `SyncEngine` in `SpendCore` (unit tested) + `SyncCoordinator` | Data is backed up and works across devices | Done |
| 4a | `catalog.sources` in the config, logos on R2 | New banks and wallets without an app release | Done |
| 4b | `catalog.tags`, `parser` keywords in the config | New default tags and parser fixes without an app release | Planned |
| 5 | Sign in with Apple (`auth/apple`) | Needs a paid Apple Developer account | Planned |
| 6 | Account deletion, `minVersion` enforcement, monitoring and alerts | Ready for the App Store | Planned |

## 12. Revisit when bigger

- **> 50k users or frequent config changes:** put bootstrap behind a CDN (configs per group, not per user), split out
  a config service.
- **Many devices editing at once:** move from last-write-wins to field-level merge.
- **A real web or mini app:** add `X-Platform: web` and its own API catalog; it may need read APIs
  (`/v1/transactions?month=…`) instead of full sync.
- **Realtime needed:** a silent push notification telling the app to pull, instead of waiting for the next interval.
