# Architecture and invariants

## Bounded domain

One organization, USD, one monthly CPM contract line per advertiser. All dates are calendar-month identifiers, not local-time instants. Rates are dollars per thousand impressions with at most six decimal places. Amounts are cents; positive values only. Credits are explicit positive documents, not negative invoice lines. Every invoice must have a positive total.

The contract cap is a monthly net billed cap, including active drafts and issued invoices less issued credits. The service serializes checks on the period row. Historical contract changes and proration are outside this release. The source adapter is a seeded PostgreSQL usage fixture; the immutable invoice snapshot records quantity, source revision, contract, rate, template version, and rounding policy.

## Transaction boundary

Each command authenticates a current active user and checks its service-layer role. It inserts `(actor, operation, key, request_hash)` with `ON CONFLICT DO NOTHING` inside the business transaction. A duplicate waits on the unique index; committed identical content replays the recorded JSON response. Different content is rejected. A failed transaction leaves neither effect nor key. Responses are stored as PostgreSQL `json`, preserving response formatting. There is no key expiry.

The transaction then locks the period `FOR UPDATE`. Every business mutation, reconciliation, worker completion, and close uses the same period-first lock order. This intentionally serializes a month's small demonstration workload. It is a correctness choice, not a scalability claim. A database uniqueness constraint prevents duplicate active original invoices; supplemental documents have an active-only source-revision identity. Voided/cancelled documents release that identity for a replacement. Usage import writes also lock the period and advance its generation; updates advance source revision.

## Approval lifecycle

`draft → pending → approved → issued → void`. Rejecting an invoice returns it to draft with a new content revision. Revising a draft or approved invoice increments both revision and concurrency version, updates the snapshot, and resets the state to draft. A pending invoice cannot be edited. Its creator or a controller may withdraw unissued work with a reason (`cancelled`). Pending/approved credit requests can likewise be cancelled. Stale approval attempts then fail with a conflict. An original cannot be voided while any active supplemental document remains; resolve those documents first.

A frozen approval contains the document revision, SHA-256 hash of financial content, initial creator, all material editors, and required reviewer count. The issue transaction verifies the hash again. Database triggers forbid approval-content edits and require decision evidence before completing an approval. Decisions are append-only, reviewer-unique, reject inactive/ineligible users and makers, and require distinct controllers for large credits. Credits have immutable requested content and one immutable memo per request.

## Journal and numbering

- Invoice: Dr AR 1100 / Cr billed revenue 4000.
- Credit: Dr sales credits 4900 / Cr AR 1100.
- Void: Dr billed revenue 4000 / Cr AR 1100.

The entry-level deferred constraint rejects empty entries. Line-level deferred constraints enforce positive equal debit and credit sums, even if constraint mode changes within the transaction. Journal amounts use `numeric` aggregate intermediates. Lines may be inserted only in their parent entry's creation transaction, identified by its database-generated transaction ID. Runtime identities cannot assign that ID, update/delete history, truncate tables, or alter the schema. Unique event keys and partial indexes prevent repeated issue and void postings.

A counter row is incremented within issuance. Failure rolls it back with the invoice, journal, outbox, and audit record. Void numbers remain present. The global verifier excludes voided invoices from outstanding AR and checks posting amounts, recipes, credit aggregates, numbering, and required events. It runs under one repeatable-read snapshot. Close runs verification while holding the period mutation lock.

These controls protect against application mistakes within scoped runtime permissions. Database owners remain trusted. They can bypass schema controls; this release makes no administrator-proof audit claim.

## Delivery protocol

Payloads describe the complete current document state and have monotonically increasing per-document sequence numbers. Allocation happens under the period lock. The worker claims one due row with `SKIP LOCKED`, a random claim token, and a 15-second lease. HTTP calls have a five-second timeout and run outside a database transaction. Completion conditionally updates the exact token while its lease is valid; stale owners cannot write completion state.

Prior delivery versions must be confirmed before newer ones are sent. Credits require a confirmed original invoice, and the receiver checks that the dependency exists. A receiver outage or ambiguous acceptance causes retry. Eight failed claims mark a row dead. The retry delay is bounded exponential backoff; ticks process at most ten rows. Local fixed-interval workers and hosted bounded jobs call the same implementation.

Receiver behavior is explicit: one record per external document ID; higher versions replace lower ones; the same version with another payload hash is rejected; an obsolete version is a successful no-op. A reviewed resync allocates a newer version. Therefore a stale issue request cannot undo a newer void. A real ERP adapter would need its own version/operation identity, conflict, visibility, authentication, pagination, and ambiguous-outcome design. An interface alone is not proof of replaceability.

## Reconciliation and close

A complete ERP read is bounded to a single synthetic snapshot. Failure or duplicate source keys aborts the run before break changes commit. Expected documents are the latest immutable outbox versions. Usage comparisons sum issued original and supplemental impressions; goodwill credits do not change delivered quantity.

A completed run persists the usage rows/revisions/billed quantities, expected latest documents, actual ERP documents, month and generation as evidence, along with their hash, findings and cutoff time. A full outer usage comparison detects billed customers whose source row is missing. It updates persistent break identities; unresolved differences stay open, resolved differences disappear, and recurrence reopens and increments the occurrence count. There is no manual 'mark resolved' or waiver button. Repair requires changing source state through a real workflow and comparing again.

Close holds the same period lock as mutations, checks every draft/pending/approved invoice and credit request, requires all deliveries confirmed, no open breaks, a clean reconciliation of the current generation younger than five minutes, and a passing verifier. The close record references that run. A later external change can create a break in a closed period; reconciliation and integration repair remain available without reopening financial posting. Financial corrections require an audited controller reopen in this release.

## Security and deployment

Opaque session secrets are stored as hashes; sessions expire after eight hours. Production login uses OIDC authorization code flow, PKCE S256, signed token validation, issuer/audience/nonce checks, verified email, and an explicitly provisioned subject/email mapping. Production startup fails without OIDC configuration. There is no just-in-time role provisioning or automatic email-domain trust.

Cookies are HttpOnly, Secure on HTTPS origins, and SameSite Strict. Mutation requests require the configured exact origin and a CSRF token. Demo personas and simulator controls require explicit demo configuration; a public synthetic demo requires an additional flag. Browser output is React-rendered text with a restrictive CSP. The application exposes no customer-defined templates or arbitrary URLs.

Cloud Run uses separate service accounts, scoped secret access, a non-public IAM-protected ERP service, and an OAuth-authenticated Scheduler-to-Job invocation. Application-to-ERP requests include Google's IAM ID token in `X-Serverless-Authorization` and a separate receiver token in `Authorization`. Hosted schema migration and identity provisioning are explicit release steps; Terraform does not execute a migration or make a database safe by itself.
