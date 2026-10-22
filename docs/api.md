# HTTP contract

All financial actions go through `POST /api/commands`. The API is same-origin and single-organization. Mutations require a current session, `Origin` equal to `APP_ORIGIN`, `X-CSRF-Token` from `/api/me`, and an `Idempotency-Key` of 8–128 characters. Requests accept one JSON object, reject unknown fields, and are limited to 16 KiB. Body and header timeouts are bounded.

`GET /api/config` returns whether synthetic demo login is enabled. `GET /api/me` returns the current user and CSRF token; neither endpoint returns session secrets. `GET /api/state?month=2026-10-01` returns a consistent period view and verifier results. The API serializes rates as strings and bounded integer cents as JSON integers.

```json
{"operation":"preview","month":"2026-10-01","customer":"northstar"}
```

Preview is persisted as a draft with a frozen usage/contract snapshot. Responses contain the new invoice ID and exact cents. Submit, revise, issue, void, and credit commands require the invoice's current `version` from state.

| Operation | Required fields beyond operation/month | Role |
|---|---|---|
| preview | customer | analyst/controller |
| revise | invoice_id, version, rate, reason | analyst/controller |
| discard | invoice_id, version, reason | creator or controller |
| cancel_credit | credit_id, reason | creator or controller |
| submit | invoice_id, version | analyst/controller |
| decide | approval_id, accept, reason when rejecting | eligible non-maker approver/controller |
| issue | invoice_id, version | controller |
| credit | invoice_id, version, amount_cents, reason | analyst/controller |
| apply_credit | credit_id | controller |
| void | invoice_id, version, reason | controller |
| supplement | customer, invoice_id, reason | analyst/controller |
| reconcile | none | controller |
| resync | document_id, reason | controller |
| retry | outbox_id, reason | controller |
| close/reopen | reason | controller |

All listed operations require an existing month. Finance mutations require an open period; reconciliation, resync, and retry can operate after close. `reason` requires at least five characters where enforced. The supplement workflow handles positive additional impressions only; a repeated source revision cannot create another active supplemental document. Voided/cancelled documents release their source identity. An original cannot be voided until its active supplements are resolved. Withdrawn approvals return a conflict. It does not automatically issue anything.

`POST /auth/demo` selects a predefined user in explicit demo mode. `GET /auth/login` starts OIDC; `/auth/callback` validates and consumes a one-time state. `POST /auth/logout` revokes the current session and requires origin/CSRF.

`POST /api/tick` and `/api/simulator` are demo-only controller tools with origin and CSRF protection. `/internal/tick` is disabled without a strong `WORKER_TOKEN`, and requires that bearer token when enabled. Hosted deployment runs the worker as a Job and leaves this endpoint disabled.

Errors use RFC 9457-shaped `application/problem+json` with status, title and detail. Conflicts and stale versions return 409; changed idempotency content returns 422; forbidden roles/CSRF return 403; missing sessions return 401. Unexpected database errors return a bounded 503 response; retry the identical command with the same key. Authentication callback responses use plain HTTP errors. A frozen OpenAPI/code-generation pipeline is not included in this release.
