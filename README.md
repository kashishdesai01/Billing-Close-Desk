# Billing Close Desk

A finance operations console that turns delivered advertising usage into approved invoices, controlled credits, and a reconciled monthly close.

## Features

- **Usage-based billing:** monthly CPM pricing with exact six-decimal rates, frozen usage snapshots, and rounding to cents.
- **Approval workflows:** revision-bound decisions, separation of duties, visible financial evidence, and withdrawal of unissued work.
- **Credits and corrections:** supplemental invoices, credit requests, dual-controller approval for credits above $250, and journaled voids.
- **Accounting:** balanced Accounts Receivable postings, immutable issued documents, and protected journal history.
- **ERP delivery:** transactional outbox, leased worker claims, retry handling, and version-aware document delivery.
- **Reconciliation:** compare delivered usage with invoiced quantities and application records with an ERP snapshot; track and repair discrepancies.
- **Period close:** finalize a month against confirmed deliveries, current reconciliation results, and accounting checks. Controllers can reopen with an audited reason.
- **Access control:** analyst, approver, controller, and viewer roles; OIDC sign-in, opaque sessions, and CSRF protection.

## Technology

| Layer | Technology |
|---|---|
| Console | React 19, TypeScript, Vite, Lucide |
| Application and worker | Go, PostgreSQL, pgx |
| Authentication | OpenID Connect, OAuth 2.0 with PKCE |
| Local environment | Docker Compose, synthetic advertisers and ERP simulator |
| Testing | Go race detector, Vitest, Playwright |
| Deployment configuration | Terraform, Google Cloud Run, Cloud SQL, Secret Manager, Cloud Scheduler |
| CI | GitHub Actions |

## Run locally

Install Docker with Compose, then run:

```sh
git clone https://github.com/kashishdesai01/Billing-Close-Desk.git
cd Billing-Close-Desk
docker compose up --build -d
python3 scripts/wait.py
```

Open **http://127.0.0.1:8090**. Compose creates the database, applies migrations, loads sample advertisers and usage, and starts the console, worker, and ERP simulator.

Choose a demo persona:

| Person | Role | Typical actions |
|---|---|---|
| Maya | Analyst | Prepare invoices and request credits |
| Alex | Approver | Review and approve eligible documents |
| Sam and Jules | Controllers | Issue documents, approve large credits, reconcile, and close |
| Guest observer | Viewer | Browse records |

For a complete scripted workflow on an untouched sample month:

```sh
python3 scripts/demo.py --month 2026-09-01
```

The walkthrough prepares four invoices, obtains approvals, issues them, recovers an interrupted ERP delivery, applies a credit approved by two controllers, reconciles, and closes September. Use `--leave-open` to leave a discrepancy for an interactive repair walkthrough.

```sh
python3 scripts/demo.py --month 2026-10-01 --leave-open
python3 scripts/recover.py --month 2026-10-01
```

Each walkthrough uses an untouched month. To stop the stack while preserving its PostgreSQL volume:

```sh
docker compose down
```

## Architecture

```mermaid
flowchart LR
    Browser[React console] --> API[Go API]
    API --> DB[(PostgreSQL)]
    Worker[Delivery worker] --> DB
    Worker --> ERP[ERP simulator]
    API --> ERP
    API --> OIDC[OIDC provider]
```

The Go API embeds the console. PostgreSQL stores source usage, contracts, document snapshots, decisions, journal entries, outbox deliveries, reconciliation runs, and audit events. Business commands commit their effects and idempotency responses together. Period locks coordinate financial changes and close decisions.

The worker sends committed outbox documents over HTTP. Claim tokens fence completion writes, and receiver versions preserve document ordering during retries. The local ERP simulator runs as a separate service with its own schema and runtime role.

See [architecture](docs/architecture.md), [HTTP API](docs/api.md), and [operations](docs/runbook.md).

## Development

Requirements: Go **1.26.10+**, Node **22.12+**, Python **3**, and PostgreSQL **14+**. Compose and CI use PostgreSQL 16.

For a native setup, create an empty database and configure the connection and local service addresses in `.env.example`:

```sh
cp .env.example .env
# Edit .env for your database, then load it into the shell.
set -a
. ./.env
set +a
make build
bin/desk -mode migrate
bin/desk -mode seed
bin/erpsim
```

In a second terminal, load the same environment and start `bin/desk`. In a third, run `bin/desk -mode worker` for continuous delivery. Migrations and seed use an owner connection; application processes switch to restricted runtime roles. The operations guide covers hosted credentials and migration grants.

## Tests

Install frontend dependencies and configure a PostgreSQL login that can create disposable test databases:

```sh
npm --prefix web ci
export TEST_DATABASE_URL='postgres://postgres:password@127.0.0.1:5432/postgres?sslmode=disable'
make test
```

`make test` runs Go vet, race-enabled backend tests, formatting checks, frontend unit tests, TypeScript checks, and the production console build.

For the complete browser and HTTP service workflow:

```sh
cd web
npx playwright install chromium
cd ..
make e2e
```

The end-to-end runner builds both services, creates and seeds a disposable database, runs the browser scenarios and delivery-recovery walkthroughs, and checks the resulting books. Results are written to `test-results/e2e/`. Set `CHROME_PATH` to use an installed Chrome executable.

To check the books of a running local database with its environment loaded:

```sh
bin/desk -mode verify
```

## Project layout

```text
cmd/          API and ERP service entry points
internal/     Billing, authentication, HTTP, storage, and worker packages
migrations/   Ordered PostgreSQL migrations
web/          Console, component tests, and browser scenarios
tests/        Database, concurrency, identity, and recovery tests
scripts/      Local walkthrough and end-to-end tooling
deploy/       Terraform configuration
docs/         Architecture, API, and operations guides
```
