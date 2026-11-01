# Operating ting-backend

The detailed reference behind the [README](../README.md): every API, how to publish config and the money source
catalog, how releases are built and deployed, and the one-time Google Cloud and GitHub setup.

- [API reference](#api-reference)
- [Managing the config](#managing-the-config)
- [Deploying](#deploying)
- [Environment variables](#environment-variables)
- [Tests with Postgres](#tests-with-postgres)

## API reference

| API | Description |
|---|---|
| `GET /v1/bootstrap` | Returns the active config. Sends an `ETag`; a matching `If-None-Match` gets `304`. No active config: `503 {"error":"configUnavailable"}` |
| `POST /v1/auth/google` | Body `{"idToken"}` from Google Sign-In, header `X-Device-Id` (a UUID the app creates). Returns `accessToken` (15 min), `refreshToken` (30 days) and `user` |
| `POST /v1/auth/refresh` | Body `{"refreshToken"}`. Returns a new token pair; the old refresh token stops working. Reusing an old token signs the device out (`refreshTokenReused`) |
| `POST /v1/auth/logout` | Body `{"refreshToken"}`. Always `204` |
| `GET /v1/me` | Needs `Authorization: Bearer <accessToken>`. Returns `{"id","email"}` |
| `POST /v1/sync/push` | Signed in. Body `{"changes": {"transactions", "tags", "sources"}}`, up to 500 records. A record is written only if its `updatedAt` is newer than the server's copy; older or equal is ignored. Returns `{"accepted", "ignored", "rejected": [{"type","id","reason"}]}` |
| `GET /v1/sync/pull?cursor=&limit=` | Signed in. Returns every record with `rev > cursor` (soft-deleted ones included), up to 1000, with `nextCursor` and `hasMore` |
| `GET /health` | `204`, for liveness checks |

Per-IP rate limits (per minute): bootstrap 30, `/v1/auth/*` 10, everything else 120. Over the limit returns
`429 {"error":"rateLimited"}` with a `Retry-After` header (seconds).

Sign-in and sync are on only when both `DATABASE_URL` and `TING_GOOGLE_CLIENT_IDS` are set. Errors look like
`{"error": "<code>"}`, for example `invalidIdToken`, `invalidRefreshToken`, `invalidAccessToken`.

The app must refresh tokens one at a time (never in parallel), because each refresh token works only once.

## Managing the config

The config is a JSON file: `configs/local.json` for local runs, and `configs/prod.json` for production. `prod.json`
contains your real domain and bucket, so it is git-ignored; create it from `configs/prod.example.json`. Every change is stored as a new version
in the `configs` table; old versions are never edited. After you activate a version the app gets it within about
30 seconds (server cache) plus the app's own `ttlSeconds` (6 hours today).

```bash
go run ./cmd/tingctl validate configs/prod.json            # check only, no database needed
export DATABASE_URL="$(cat ~/ting-neon-url.txt)" TING_ALLOWED_API_HOSTS=<your-api-host>
go run ./cmd/tingctl list                                  # * marks the active version
go run ./cmd/tingctl publish configs/prod.json "what changed"   # stores a new version, not active yet
go run ./cmd/tingctl activate <version>
```

Roll back with `tingctl activate <old version>`.

The server and `tingctl` reject a config whose `api.baseUrl` is not on `TING_ALLOWED_API_HOSTS`
(default `*.run.app,localhost,127.0.0.1`) or not https (except localhost), whose endpoints are malformed, or whose
catalog is invalid (see below).

### Publishing by hand in SQL

`tingctl` runs two statements, so you can do the same in the Neon SQL editor. You skip validation, so run
`tingctl validate` on the file first.

```sql
-- 1. Store a new version (not active yet); returns the version number.
INSERT INTO configs (schema_version, payload, note)
VALUES (1, $cfg${ …contents of configs/prod.json… }$cfg$::jsonb, 'what changed')
RETURNING version;

-- 2. Activate it. Only one row may be active, so turn the old one off first.
BEGIN;
UPDATE configs SET is_active = false WHERE is_active;
UPDATE configs SET is_active = true WHERE version = <N>;
COMMIT;
```

Don't edit the `payload` of the active row in place: insert a new row instead, so you can roll back.

### Money source catalog

`catalog.sources` in the config lists the default money sources the app installs (e-wallets, banks, cash):

| Field | Meaning |
|---|---|
| `id` | Fixed UUID. Never change it: it is how devices avoid duplicates when syncing. The 20 built-in sources use the same IDs as the app's `DefaultPaymentSources` |
| `name` | Display name |
| `kind` | `wallet`, `bank`, `cash` or `other` |
| `sortOrder` | Order within its kind |
| `logoUrl` | Full https URL of the logo, on a host in `TING_ALLOWED_LOGO_HOSTS` (default `*.r2.dev`) |
| `aliases` | Lowercase letters and digits, no spaces or diacritics. Used to match source names coming from Shortcuts, e.g. `vcb` → Vietcombank |

`catalog.sources.version` tells the app there is something new. The app only **adds** sources whose `id` it has never
seen (deleted ones count as seen) and whose name isn't already taken; it never edits or removes sources the user has.

### Logos on Cloudflare R2

The app loads logos straight from a public R2 bucket, not through this server. The source files are in
`assets/logos/`. Point `logoUrl` at your own public bucket, e.g. `https://pub-<bucket-id>.r2.dev`.

Upload or re-upload the logos:

```bash
npx wrangler login
make upload-logos BUCKET=ting-logos
```

Add a new bank or wallet without shipping a new app build:

1. Add `assets/logos/<name>.png` (square PNG) and run `make upload-logos BUCKET=ting-logos`.
2. Add an item to `configs/prod.json` with a **new** `id`, `logoUrl: "https://pub-….r2.dev/logos/<name>.png"` and
   its aliases. Bump `catalog.sources.version`.
3. Validate, publish and activate the config.

To change an existing logo, upload it under a new file name and update `logoUrl`: the CDN and the app cache by URL.

Setting up a new bucket: Cloudflare → R2 → **Create bucket** → **Settings** → **Public access** → turn on the
**R2.dev subdomain** (or attach a custom domain, then add it to `TING_ALLOWED_LOGO_HOSTS` and to the app's
`TingLogoHosts`).

## Deploying

Releases go through GitHub Actions (`.github/workflows/deploy.yml`). Pushing a version tag builds the image; deploying
it is a manual step.

```bash
git tag v0.4.0 && git push origin v0.4.0
```

1. The workflow runs the tests, then builds `us-central1-docker.pkg.dev/<PROJECT_ID>/ting/ting-backend:v0.4.0`
   and pushes it to Artifact Registry.
2. To deploy: GitHub → **Actions** → pick the **deploy** workflow in the left column (not "All workflows") →
   **Run workflow** → branch `main`, `version` = `v0.4.0` → **Run workflow**. Only people with write access to the
   repo see the button.
3. The deploy job deploys that image by digest to the `ting-backend` Cloud Run service and checks `/health`.

Rolling back is the same with an older version. You can also switch traffic back to an earlier revision in the
Cloud Run console (**Revisions** tab).

The config is not published by the workflow; the workflow only validates `configs/local.json` and `configs/prod.example.json`
(the real `prod.json` isn't in the repo, so validate it locally before publishing). Publish it with
`tingctl` as described above.

| Trigger | What runs |
|---|---|
| Push to `main`, pull request | `go vet`, `go test` against a real Postgres, `tingctl validate` on `configs/local.json` and `configs/prod.example.json` |
| Push a tag `vX.Y.Z` | Tests, then build a `linux/amd64` image tagged `vX.Y.Z` and push it to Artifact Registry |
| **Run workflow** with `version = vX.Y.Z` | Deploy that image to Cloud Run (by digest), then check `/health` |

Deploying only swaps the image. Environment variables, secrets and scaling stay as configured on the running
service, so change those in the Cloud Run console.

GitHub's **Required reviewers** approval isn't available for private repos on the Free plan, so the manual
**Run workflow** step is the approval.

### How GitHub Actions signs in to Google Cloud

The workflow uses **Workload Identity Federation**, so no service account key is stored anywhere:

1. The job asks GitHub for an OIDC token (`permissions: id-token: write`). GitHub signs it, and it states the repo,
   ref, commit and actor. It is valid for a few minutes.
2. `google-github-actions/auth` sends that token to Google's Security Token Service. The `github` provider checks
   GitHub's signature and the attribute condition: repository `<owner>/<repo>`, ref a `v…` tag or `main`.
3. Google lets that repo impersonate the `github-deploy` service account (`roles/iam.workloadIdentityUser`) and
   returns an access token valid for about an hour.
4. `github-deploy` can only push images to the `ting` repository and deploy Cloud Run revisions.

None of the GitHub variables below are secret: they are addresses, not credentials. A fork, another branch, or someone
who knows the values can't get a token, because they can't produce a GitHub-signed token for this repo and ref.
Anyone who can push to `main` or push a `v…` tag can deploy, so protect those if you add collaborators.

To revoke access, remove the `workloadIdentityUser` binding on `github-deploy` or disable the provider:

```bash
gcloud iam workload-identity-pools providers disable github --location=global --workload-identity-pool=github
```

### One-time Google Cloud setup

Run in Cloud Shell:

```bash
PROJECT_ID=<your-project-id>
PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format='value(projectNumber)')
REGION=us-central1
GITHUB_REPO=<owner>/<repo>          # the GitHub repo that runs the deploy workflow
DEPLOYER=github-deploy@$PROJECT_ID.iam.gserviceaccount.com
gcloud config set project $PROJECT_ID

gcloud services enable artifactregistry.googleapis.com run.googleapis.com iamcredentials.googleapis.com sts.googleapis.com

# Image repository
gcloud artifacts repositories create ting --repository-format=docker --location=$REGION

# Service account for GitHub: push images, deploy Cloud Run
gcloud iam service-accounts create github-deploy --display-name="GitHub deploy"
gcloud artifacts repositories add-iam-policy-binding ting --location=$REGION \
  --member="serviceAccount:$DEPLOYER" --role=roles/artifactregistry.writer
gcloud projects add-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:$DEPLOYER" --role=roles/run.developer
# Let it deploy revisions that run as the Cloud Run service account (the default compute SA)
gcloud iam service-accounts add-iam-policy-binding $PROJECT_NUMBER-compute@developer.gserviceaccount.com \
  --member="serviceAccount:$DEPLOYER" --role=roles/iam.serviceAccountUser

# Trust GitHub Actions from this repo only, from v… tags (build) or main (deploy)
gcloud iam workload-identity-pools create github --location=global --display-name="GitHub"
gcloud iam workload-identity-pools providers create-oidc github --location=global \
  --workload-identity-pool=github --issuer-uri=https://token.actions.githubusercontent.com \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
  --attribute-condition="assertion.repository=='$GITHUB_REPO' && (assertion.ref.startsWith('refs/tags/v') || assertion.ref=='refs/heads/main')"
gcloud iam service-accounts add-iam-policy-binding $DEPLOYER --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github/attribute.repository/$GITHUB_REPO"

echo "WIF_PROVIDER=projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github/providers/github"
echo "WIF_SERVICE_ACCOUNT=$DEPLOYER"
```

If the Cloud Run service runs as a custom service account, grant `roles/iam.serviceAccountUser` on that one instead
of the compute SA.

### One-time GitHub setup

Repo → **Settings** → **Secrets and variables** → **Actions** → **Variables** tab → **Repository variables**
(not environment variables, not secrets):

| Variable | Value |
|---|---|
| `GCP_PROJECT_ID` | `<PROJECT_ID>` |
| `GCP_REGION` | `us-central1` |
| `GAR_REPOSITORY` | `ting` |
| `CLOUD_RUN_SERVICE` | `ting-backend` |
| `WIF_PROVIDER` | `projects/<project number>/locations/global/workloadIdentityPools/github/providers/github` |
| `WIF_SERVICE_ACCOUNT` | `github-deploy@<PROJECT_ID>.iam.gserviceaccount.com` |

If `google-github-actions/auth` fails with *must specify exactly one of "workload_identity_provider" or
"credentials_json"*, a variable is missing or was created in the wrong place.

### Cloud Run service

The service already exists. For reference, or to recreate it:

| Setting | Value | Why |
|---|---|---|
| Service name | `ting-backend` | |
| Region | `us-central1` | Tier 1 region, full free tier |
| Container image | `us-central1-docker.pkg.dev/<PROJECT_ID>/ting/ting-backend:<version>` | |
| Authentication | **Allow public access** | The app calls it directly; the API checks tokens itself |
| Billing | **Request-based** | CPU only runs during requests |
| Scaling | Auto, **min 0**, **max 2** | Scales to zero when idle; caps cost under abuse |
| Container port | `8080` | |
| CPU / memory | 1 vCPU / **256 MiB** | |
| Request timeout | 30 s | |
| Variables | `TING_GOOGLE_CLIENT_IDS` = iOS OAuth client ID | |
| Secrets as env vars | `DATABASE_URL` → `ting-database-url`, `TING_JWT_SECRET` → `ting-jwt-secret` (latest) | Grant the service account access when the console asks |
| Startup probe (optional) | HTTP `/health` | |

Secrets in Secret Manager:

| Secret | Value |
|---|---|
| `ting-database-url` | Neon connection string for role `ting_app`, host **without** `-pooler`, with `sslmode=require` |
| `ting-jwt-secret` | `openssl rand -hex 32`, different from your local one |

The image contains no secrets. To build one by hand (for example before the first workflow run), build for
`linux/amd64`, since Cloud Run can't run the ARM images a Mac builds by default:

```bash
gcloud auth configure-docker us-central1-docker.pkg.dev
docker buildx build --platform linux/amd64 \
  -t us-central1-docker.pkg.dev/<PROJECT_ID>/ting/ting-backend:manual-1 --push .
```

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Set by Cloud Run |
| `DATABASE_URL` | | Postgres. Required unless `TING_CONFIG_FILE` is set |
| `TING_CONFIG_FILE` | | Serve the config from a file, for local runs |
| `TING_ALLOWED_API_HOSTS` | `*.run.app,localhost,127.0.0.1` | Hosts `api.baseUrl` may point to |
| `TING_ALLOWED_LOGO_HOSTS` | `*.r2.dev` | Hosts `catalog.sources[].logoUrl` may point to. Add a custom logo domain here |
| `TING_GOOGLE_CLIENT_IDS` | | The app's OAuth client IDs. Set to turn on Google sign-in |
| `TING_JWT_SECRET` | | Access token signing key, at least 32 characters (`openssl rand -hex 32`) |

## Tests with Postgres

```bash
go test ./...
```

With a real Postgres (sign-in and sync stores):

```bash
docker run -d --rm --name ting-pg-test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=ting -p 55432:5432 postgres:16-alpine
TING_TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/ting?sslmode=disable' go test ./...
docker stop ting-pg-test
```
