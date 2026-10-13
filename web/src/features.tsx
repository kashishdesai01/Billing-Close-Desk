import React from "react";
import {
  RefreshCw,
  CheckCheck,
  Check,
  GitCompareArrows,
  ShieldCheck,
  BookOpen,
  Plug,
} from "lucide-react";
import { money, type State, type User } from "./model";
import { Badge } from "./ui";
type Props = {
  data: State;
  user: User;
  controller: boolean;
  isOpen: boolean;
  busy: boolean;
  demo: boolean;
  command: (c: Record<string, unknown>) => Promise<boolean>;
  special: (path: string, c?: Record<string, unknown>) => Promise<void>;
  customerName: (id: string) => string;
};
export function Approvals({
  data,
  user,
  controller,
  isOpen,
  busy,
  command,
  special,
  customerName,
  demo,
}: Props) {
  const pending = data.approvals.filter((a) => a.state === "pending");
  return (
    <section className="panel">
      <div className="panel-title">
        <div>
          <h2>Decision queue</h2>
          <p>
            Creators cannot approve their own requests. Large credits require
            two distinct controllers.
          </p>
        </div>
        <span className="count">{pending.length} pending</span>
      </div>
      {pending.length === 0 ? (
        <div className="empty">
          <CheckCheck />
          <h3>You’re all caught up</h3>
          <p>Submit an invoice or request a credit to create a review.</p>
        </div>
      ) : (
        pending.map((a) => (
          <div className="approval-row" key={a.id}>
            <span className="customer-initial">
              {customerName(a.customer_id)[0]}
            </span>
            <div>
              <h3>{a.customer_name}</h3>
              <p>
                {a.document_type === "credit"
                  ? "Credit request"
                  : "Invoice approval"}{" "}
                · {a.votes}/{a.required} decisions · creator {a.creator}
              </p>
              <code>{a.document_id}</code>
              <div className="approval-evidence" aria-label="Approval evidence">
                <strong>
                  Revision {a.revision} ·{" "}
                  {a.document_type === "credit" ? "Credit request" : "Invoice"}
                </strong>
                {a.document_type === "credit" ? (
                  <>
                    <p>Reason: {a.credit_reason}</p>
                    <p>
                      Linked invoice:{" "}
                      {a.linked_invoice_number
                        ? `INV-${String(a.linked_invoice_number).padStart(5, "0")}`
                        : a.invoice_id}
                    </p>
                  </>
                ) : (
                  <>
                    <p>
                      {Number(a.invoice_snapshot?.impressions).toLocaleString()}{" "}
                      impressions · ${String(a.invoice_snapshot?.rate)} CPM
                    </p>
                    <p>
                      Usage source revision{" "}
                      {String(a.invoice_snapshot?.usage_revision)} · contract{" "}
                      {String(a.invoice_snapshot?.contract_id)}
                    </p>
                    {a.invoice_snapshot?.adjustment_reason && (
                      <p>
                        Adjustment:{" "}
                        {String(a.invoice_snapshot.adjustment_reason)}
                      </p>
                    )}
                  </>
                )}
                <small>Content fingerprint: {a.content_hash}</small>
              </div>
            </div>
            <strong>{money(a.amount_cents)}</strong>
            {["approver", "controller"].includes(user.role) &&
            a.creator !== user.id &&
            !a.makers.includes(user.id) &&
            !a.voters.includes(user.id) &&
            (a.required === 1 || controller) &&
            isOpen ? (
              <div className="actions">
                <button
                  disabled={busy}
                  className="button"
                  onClick={() => {
                    const reason = window.prompt("Reason for rejection");
                    if (reason)
                      command({
                        operation: "decide",
                        approval_id: a.id,
                        accept: false,
                        reason,
                      });
                  }}
                >
                  Reject
                </button>
                <button
                  disabled={busy}
                  className="button primary"
                  onClick={() =>
                    command({
                      operation: "decide",
                      approval_id: a.id,
                      accept: true,
                    })
                  }
                >
                  Approve
                  <Check size={15} />
                </button>
              </div>
            ) : (
              <span className="muted">
                {a.creator === user.id || a.makers.includes(user.id)
                  ? "Another person must review"
                  : "Awaiting eligible reviewer"}
              </span>
            )}
          </div>
        ))
      )}
      <div className="panel-title">
        <h2>Credit requests</h2>
      </div>
      {data.credits.length === 0 ? (
        <p className="padded muted">No credit requests this period.</p>
      ) : (
        data.credits.map((c) => (
          <div className="approval-row" key={c.id}>
            <div>
              <h3>{c.reason}</h3>
              <code>{c.id.slice(0, 12)}</code>
            </div>
            <Badge state={c.state} />
            <strong>{money(c.amount_cents)}</strong>
            {isOpen &&
              ["pending", "approved"].includes(c.state) &&
              (controller || c.created_by === user.id) && (
                <button
                  className="button"
                  disabled={busy}
                  onClick={() => {
                    const reason = window.prompt(
                      "Reason for cancelling this credit request",
                    );
                    if (reason)
                      command({
                        operation: "cancel_credit",
                        credit_id: c.id,
                        reason,
                      });
                  }}
                >
                  Cancel credit
                </button>
              )}
            {controller && isOpen && c.state === "approved" && (
              <button
                disabled={busy}
                className="button primary"
                onClick={() =>
                  command({ operation: "apply_credit", credit_id: c.id })
                }
              >
                Issue credit
              </button>
            )}
          </div>
        ))
      )}
    </section>
  );
}
export function Reconciliation({
  data,
  user,
  controller,
  isOpen,
  busy,
  command,
  special,
  customerName,
  demo,
}: Props) {
  return (
    <section className="panel">
      <div className="panel-title">
        <div>
          <h2>Reconciliation workspace</h2>
          <p>
            Delivered impressions ↔ issued invoices · application documents ↔
            ERP snapshot
          </p>
        </div>
        {controller && (
          <button
            className="button primary"
            disabled={busy}
            onClick={() => command({ operation: "reconcile" })}
          >
            <RefreshCw size={15} />
            Run reconciliation
          </button>
        )}
      </div>
      {data.breaks.length === 0 ? (
        <div className="empty">
          <GitCompareArrows />
          <h3>
            {data.recon_runs.length
              ? "No discrepancies found"
              : "Compare the records"}
          </h3>
          <p>
            Each completed run records source evidence and the period
            generation.
          </p>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Discrepancy / subject</th>
                <th>Expected</th>
                <th>Actual</th>
                <th>State</th>
                <th>Occurrences</th>
                <th>Resolve</th>
              </tr>
            </thead>
            <tbody>
              {data.breaks.map((b) => (
                <tr key={b.id}>
                  <td>
                    <strong>{b.kind.replaceAll("_", " ")}</strong>
                    <small>{customerName(b.subject)}</small>
                  </td>
                  <td>
                    <code className="wrap-code">{b.expected}</code>
                  </td>
                  <td>
                    <code className="wrap-code">{b.actual}</code>
                  </td>
                  <td>
                    <Badge state={b.state} />
                  </td>
                  <td>{b.occurrences}</td>
                  <td>
                    {controller &&
                      b.state === "open" &&
                      b.kind === "erp_mismatch" && (
                        <button
                          disabled={busy}
                          className="button"
                          onClick={() => {
                            const reason = window.prompt(
                              "Reason for resending the immutable application snapshot",
                            );
                            if (reason)
                              command({
                                operation: "resync",
                                document_id: b.subject,
                                reason,
                              });
                          }}
                        >
                          Resend snapshot
                        </button>
                      )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="padded help">
        <ShieldCheck size={18} />
        <p>
          Breaks resolve after a successful source comparison confirms the
          difference is gone. A failed ERP read leaves existing breaks
          untouched. A recurring discrepancy reopens its record.
        </p>
      </div>
    </section>
  );
}
export function Journal({
  data,
  user,
  controller,
  isOpen,
  busy,
  command,
  special,
  customerName,
  demo,
}: Props) {
  return (
    <section className="panel">
      <div className="panel-title">
        <div>
          <h2>AR subledger</h2>
          <p>
            Billing-derived postings; this demonstration does not implement
            revenue recognition or payments.
          </p>
        </div>
        <Badge state={data.verification.passed ? "verified" : "failed"} />
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Posting / advertiser</th>
              <th>Account</th>
              <th className="right">Debit</th>
              <th className="right">Credit</th>
            </tr>
          </thead>
          <tbody>
            {data.ledger.map((l, index) => (
              <tr key={l.id + index}>
                <td>
                  <strong>
                    {l.kind === "issue"
                      ? "Invoice issued"
                      : l.kind === "void"
                        ? "Invoice reversed"
                        : "Credit issued"}
                  </strong>
                  <small>
                    {l.name} · {l.id.slice(0, 8)}
                  </small>
                </td>
                <td>
                  {l.account} ·{" "}
                  {l.account === "1100"
                    ? "Accounts receivable"
                    : l.account === "4000"
                      ? "Billed revenue"
                      : "Sales credits"}
                </td>
                <td className="right amount">
                  {l.direction === "D" ? money(l.amount_cents) : "—"}
                </td>
                <td className="right amount">
                  {l.direction === "C" ? money(l.amount_cents) : "—"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!data.ledger.length && (
          <div className="empty">
            <BookOpen />
            <h3>No postings yet</h3>
            <p>
              Issue an approved invoice to record the first balanced journal
              entry.
            </p>
          </div>
        )}
      </div>
    </section>
  );
}
export function Integrations({
  data,
  user,
  controller,
  isOpen,
  busy,
  command,
  special,
  customerName,
  demo,
}: Props) {
  return (
    <>
      <section className="panel">
        <div className="panel-title">
          <div>
            <h2>ERP document delivery</h2>
            <p>
              Version-aware simulator · 15-second claims · eight attempts before
              operator recovery.
            </p>
          </div>
          {demo && controller && (
            <button
              disabled={busy}
              className="button primary"
              onClick={() => special("/api/tick")}
            >
              <RefreshCw size={15} />
              Run delivery tick
            </button>
          )}
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Document</th>
                <th>Version</th>
                <th>Status</th>
                <th>Attempts</th>
                <th>Recovery</th>
              </tr>
            </thead>
            <tbody>
              {data.outbox.map((o) => (
                <tr key={o.id}>
                  <td>
                    <strong>{o.document_type}</strong>
                    <small>{o.document_id.slice(0, 12)}</small>
                  </td>
                  <td>v{o.sequence}</td>
                  <td>
                    <Badge state={o.status} />
                  </td>
                  <td>{o.attempts}</td>
                  <td>
                    {o.last_error && <small>{o.last_error}</small>}
                    {o.status === "dead" && controller && (
                      <button
                        className="button"
                        disabled={busy}
                        onClick={() =>
                          command({
                            operation: "retry",
                            outbox_id: o.id,
                            reason: "Operator reviewed and requested retry",
                          })
                        }
                      >
                        Retry
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!data.outbox.length && (
            <div className="empty">
              <Plug />
              <h3>Ready to connect</h3>
              <p>
                Issued documents create delivery records in the same
                transaction.
              </p>
            </div>
          )}
        </div>
      </section>
      {demo && controller && (
        <section className="panel padded">
          <span className="eyebrow">CONTROLLED FAILURE LAB</span>
          <h2>See how recovery works</h2>
          <p className="muted">
            These controls affect only the synthetic ERP. Acknowledgement loss
            accepts a document, then returns an error.
          </p>
          <div className="actions">
            <button
              disabled={busy}
              className="button"
              onClick={() => special("/api/simulator", { mode: "normal" })}
            >
              Healthy receiver
            </button>
            <button
              disabled={busy}
              className="button"
              onClick={() => special("/api/simulator", { mode: "outage" })}
            >
              Simulate outage
            </button>
            <button
              disabled={busy}
              className="button"
              onClick={() =>
                special("/api/simulator", { mode: "accept_then_fail" })
              }
            >
              Lose acknowledgement
            </button>
          </div>
        </section>
      )}
    </>
  );
}
