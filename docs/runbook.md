# Operations

## Local demonstration

`docker compose up --build -d` builds the console and Go binaries, migrates and seeds PostgreSQL, then starts the API, synthetic ERP and local worker. The only published ports are the API on loopback 8090 and PostgreSQL on loopback 55432. ERP is reachable only inside the Compose network. Data is synthetic and credentials in Compose are local-only.

The scripted demo expects an untouched seeded month. August and October are reserved for isolated browser tests; September can be used for scripted close and October for a hands-on `--leave-open` run. If you need a fresh environment, create a new named Compose project and volume; do not erase a volume containing records you want to preserve.

Without Docker, build with `make build`, provide an existing empty PostgreSQL database using `.env.example`, run `bin/desk -mode migrate` and `-mode seed`, then start `bin/erpsim` and `bin/desk`. `-mode worker` is a local continuous worker. `-mode tick` performs one bounded delivery batch. Use an owner connection only for migration and seed; change runtime DSNs to non-owner login users with the granted scoped roles.

## Release to Cloud Run

The Terraform configuration deploys the API, ERP service, migration Job, worker Job and scheduler against existing infrastructure. Configure enabled Run, SQL Admin, Secret Manager, Scheduler, IAM, Artifact Registry APIs; a PostgreSQL 16 Cloud SQL instance with a database and separate non-owner app/ERP users; an owner migration user; backups/PITR; an Artifact Registry image digest; existing secret versions; a registered OIDC application; and a fixed HTTPS origin/custom domain.

1. Build/test the exact image, scan it, and push to your registry. Supply an immutable digest to Terraform. Review the plan in the intended project. Remote Terraform state with restricted access is an operator prerequisite.
2. Database DSNs in Secret Manager must use `host=/cloudsql/PROJECT:REGION:INSTANCE`; mounted Cloud SQL connections provide the authenticated encrypted transport. Do not use `sslmode=disable` with a direct remote TCP connection. The app and ERP login users must not own schemas, be superusers, or have role-management privileges.
3. Bootstrap an empty database in two stages. Keep Scheduler paused. From `deploy/terraform`, review and apply a targeted plan for the independently creatable migration Job and its prerequisites, execute it, and wait for successful completion before planning the remaining services:

   ```sh
   terraform plan -target=google_cloud_run_v2_job.migrator -out=bootstrap.tfplan
   terraform apply bootstrap.tfplan
   gcloud run jobs execute close-desk-migrator --region=YOUR_REGION --project=YOUR_PROJECT --wait
   # Continue only after the migration execution succeeds.
   terraform plan -out=release.tfplan
   terraform apply release.tfplan
   ```

   Targeting is a first-bootstrap exception, not the routine release strategy. Review both plans. The migrator grants `RUNTIME_DB_USER` and `ERP_DB_USER` their schema runtime roles. Do not run an initial full apply against an unmigrated database: API/ERP startup requires those tables and roles. For subsequent releases execute compatible migrations before shifting service traffic; validate old/new revision compatibility explicitly.
4. Provision real users with an owner connection: `users.id`, display name, role, active flag, verified email, and the exact subject from the configured issuer. No public user-management API exists. Synthetic seed is intentionally unavailable in hosted mode. Provision month/customer/contract/usage reference data through a reviewed migration or import; there is no production source ingestion adapter.
5. Register `/auth/callback`, verify OIDC login/logout, denied identities, inactive users, cookies, audience, issuer, and CSRF in the real environment. Test secret rotation by deploying a new revision; `latest` secret versions are resolved at revision startup.
6. Verify Cloud Run IAM prevents anonymous ERP invocation. Check both API and worker service accounts can reach it using IAM tokens. Exercise a real scheduled tick before unpausing Scheduler.
7. Smoke-test the root, `/healthz`, one persisted preview, independent approval, issue, delivery, reconciliation, and verifier using synthetic data. Record the image digest, migration version and service revisions.

Migrations must remain forward-compatible with the old service revision. This release has an initial migration and additive hardening migration 002. The runner serializes migration execution, records SHA-256 checksums, and rejects edits to recorded migrations. A legacy checksum-less 001 record adopts the supplied original file checksum on upgrade; this does not retrospectively prove legacy DDL. Subsequent changes need additive migrations and a release-specific compatibility review. Do not edit an already deployed migration. Roll back service revisions only when their schema contract remains valid.

The configuration has deletion protection and creates no locked-retention bucket. SQL, secrets, DNS, registry and state are external prerequisites; destroying Run resources does not delete those or stop their costs. Budget alerts do not cap spend. Review project billing and explicitly stop/delete resources you no longer need.

## Backlog or dead delivery

Check Integrations: status, attempts and age. Confirm the receiver is healthy. A lost acknowledgement is retryable; do not allocate a new document identity. A newer version waits for earlier versions, so inspect dead predecessors first. After investigating, retry a dead item with a reason. Do not set database statuses manually.

The lease is 15 seconds and receiver timeout five seconds. Hosted jobs have bounded execution; multiple ticks may overlap safely. Claims that fail to finish expire and can be reclaimed. Token fencing protects completion writes, while receiver sequence enforcement protects external document state. A real adapter requires independent validation of these receiver semantics.

## ERP mismatch

Review expected and actual records in Reconciliation. If application state is authoritative for this synthetic case, use Resend snapshot, run a delivery tick, then rerun reconciliation. Resync allocates a new delivery version; it never edits the invoice. A discrepancy is resolved only after a completed comparison agrees. Do not routinely overwrite a real ERP's edits without establishing business ownership and approval rules.

## Close blocked

Resolve all non-final invoices and credit requests, drain delivery including dead items, and clear source discrepancies. Run a fresh reconciliation after the last mutation/delivery. Check the verifier. Its diagnostic results come from a single database snapshot. Controller close records its reconciliation cutoff. Reopen only with a reason; never disable period triggers.

Downward usage correction after an already credited invoice, posting into a different current period, payments, and automated contract changes are unsupported. Stop and design an explicit supported workflow before entering such data.

## Verification failure

Stop posting, preserve database/log evidence, and investigate the named invariant. Do not 'repair' a journal row; runtime roles cannot edit it. Restore into a separate environment or add a reviewed compensating business operation. Rerun the full verifier before resuming. Backups are not a recovery drill; test restore and document recovery objectives before serving real financial workflows.

## Monitoring and capacity

Application processes use JSON logs. Audit records contain the authoritative command trail. Monitor job failures, missing scheduled executions, oldest unconfirmed outbox age, dead count, reconciliation failures and verifier failures. The repository does not include a verified alerting deployment, production SLO, or capacity benchmark. Add and exercise alerts in the target project before operational use.

Each process has at most eight database connections. Count API replicas, ERP replicas, concurrent Job executions, migrations and administrative reserve when setting a Cloud SQL limit. Period mutation intentionally serializes within a month. Never infer production throughput from the seeded demonstration.
