import { Badge } from "./ui";
import { Approvals, Reconciliation, Journal, Integrations } from "./features";
import React, { useState, useEffect, useCallback, useRef } from "react";
import { createRoot } from "react-dom/client";
import {
  LayoutDashboard,
  FileText,
  CheckCheck,
  GitCompareArrows,
  BookOpen,
  Plug,
  ArrowUpRight,
  ArrowRight,
  Check,
  ChevronDown,
  RefreshCw,
  ShieldCheck,
  Clock,
  Activity,
  LockKeyhole,
  Plus,
  LogOut,
  Search,
  X,
  AlertTriangle,
} from "lucide-react";
import { money, cents, type State, type User, type Invoice } from "./model";
import "./style.css";
type Command = Record<string, unknown>;
const personas = [
  ["maya", "Maya Chen", "Analyst"],
  ["alex", "Alex Rivera", "Approver"],
  ["sam", "Sam Okafor", "Controller"],
  ["jules", "Jules Patel", "Controller"],
  ["viewer", "Guest observer", "Viewer"],
];
const nav = [
  ["Overview", LayoutDashboard],
  ["Invoices", FileText],
  ["Approvals", CheckCheck],
  ["Reconciliation", GitCompareArrows],
  ["Journal", BookOpen],
  ["Integrations", Plug],
] as const;
function App() {
  const loadSequence = useRef(0);
  const retryKeys = useRef(new Map<string, string>());
  const [user, setUser] = useState<User | null>(null),
    [demo, setDemo] = useState(false),
    [page, setPage] = useState("Overview"),
    [month, setMonth] = useState("2026-10-01"),
    [data, setData] = useState<State | null>(null),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [selected, setSelected] = useState<Invoice | null>(null),
    [form, setForm] = useState<{ kind: string; invoice: Invoice } | null>(null),
    [value, setValue] = useState(""),
    [reason, setReason] = useState(""),
    [query, setQuery] = useState(""),
    [authReady, setAuthReady] = useState(false);
  const expireSession = useCallback((message: string) => {
    loadSequence.current++;
    retryKeys.current.clear();
    setUser(null);
    setData(null);
    setSelected(null);
    setForm(null);
    setError(message);
  }, []);
  async function logout() {
    setBusy(true);
    setError("");
    try {
      const response = await fetch("/auth/logout", {
        method: "POST",
        headers: { "X-CSRF-Token": user!.csrf },
      });
      if (!response.ok && response.status !== 401)
        throw new Error(
          "Sign-out failed. Your session may still be active; please retry.",
        );
      expireSession("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const me = useCallback(async () => {
    const config = await fetch("/api/config");
    if (config.ok) setDemo((await config.json()).demo);
    const r = await fetch("/api/me");
    if (r.ok) {
      const body = await r.json();
      setUser(body.user);
      setDemo(body.demo);
    } else if (r.status === 401) {
      setUser(null);
    } else throw new Error("Authentication service is unavailable");
    setAuthReady(true);
  }, []);
  const load = useCallback(async () => {
    const sequence = ++loadSequence.current;
    const r = await fetch("/api/state?month=" + month);
    if (r.status === 401) {
      if (sequence === loadSequence.current)
        expireSession("Your session expired. Please sign in again.");
      return;
    }
    if (!r.ok) throw new Error("Could not refresh the desk");
    const next = await r.json();
    if (sequence === loadSequence.current) setData(next);
  }, [month, expireSession]);
  useEffect(() => {
    me().catch((e) => setError(String(e)));
  }, [me]);
  useEffect(() => {
    if (user) load().catch((e) => setError(String(e)));
  }, [user, load]);
  async function login(id: string) {
    setBusy(true);
    setError("");
    try {
      const r = await fetch("/auth/demo", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ user: id }),
      });
      if (!r.ok)
        throw new Error(
          "Persona login is unavailable. Use organization sign-in for a provisioned environment.",
        );
      await me();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function command(c: Command) {
    if (busy) return false;
    const body = JSON.stringify({ month, ...c });
    let key = retryKeys.current.get(body);
    if (!key) {
      key = crypto.randomUUID();
      retryKeys.current.set(body, key);
    }
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const r = await fetch("/api/commands", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": user!.csrf,
          "Idempotency-Key": key,
        },
        body,
      });
      if (r.status === 401) {
        expireSession("Your session expired. Please sign in again.");
        return false;
      }
      const b = await r.json();
      if (!r.ok) {
        if (r.status < 500) retryKeys.current.delete(body);
        throw new Error(b.detail || "Operation failed");
      }
      retryKeys.current.delete(body);
      setNotice(
        c.operation === "reconcile"
          ? `Reconciliation finished · ${b.findings} open discrepancies`
          : "Change recorded in the audit trail",
      );
      setForm(null);
      setSelected(null);
      await load();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  }
  async function special(path: string, body: Command = {}) {
    setBusy(true);
    setError("");
    try {
      const r = await fetch(path, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": user!.csrf,
        },
        body: JSON.stringify(body),
      });
      if (r.status === 401) {
        expireSession("Your session expired. Please sign in again.");
        return;
      }
      const b = await r.json();
      if (!r.ok) throw new Error(b.detail || "Operation failed");
      setNotice(
        path.endsWith("tick")
          ? `Delivery tick finished · ${b.processed} items processed`
          : "Simulator control updated",
      );
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    if (!selected && !form) return;
    const before = document.activeElement as HTMLElement;
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    const nodes = () =>
      Array.from(
        dialog?.querySelectorAll<HTMLElement>(
          'button:not([disabled]),input,textarea,select,a[href],[tabindex="0"]',
        ) || [],
      );
    nodes()[0]?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setSelected(null);
        setForm(null);
      }
      if (e.key === "Tab") {
        const all = nodes();
        const first = all[0],
          last = all.at(-1);
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", key);
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", key);
      document.body.style.overflow = overflow;
      before?.focus();
    };
  }, [selected, form]);
  if (!user)
    return (
      <div className="login">
        <div className="login-brand">
          <BookOpen size={26} /> CLOSE DESK
        </div>
        <div className="login-card">
          <span className="eyebrow">BILLING OPERATIONS</span>
          <h1>
            Good books.
            <br />
            Clear decisions.
          </h1>
          <p>
            A working desk for the journey from delivered usage to a reconciled,
            closed period.
          </p>
          <a className="button primary" href="/auth/login">
            Organization sign-in <ArrowRight size={16} />
          </a>
          {demo && (
            <>
              <div className="divider">Synthetic demonstration</div>
              <p className="muted">
                The local demo uses fictional advertisers. Choose a role to
                explore approvals and separation of duties.
              </p>
              {personas.map(([id, name, role]) => (
                <button
                  disabled={busy || !authReady}
                  className="persona"
                  key={id}
                  onClick={() => login(id)}
                >
                  <span className="avatar">
                    {name
                      .split(" ")
                      .map((x) => x[0])
                      .join("")}
                  </span>
                  <span>
                    {name}
                    <small>{role}</small>
                  </span>
                  <ArrowRight size={16} />
                </button>
              ))}
            </>
          )}
          {error && (
            <div role="alert" className="alert">
              {error}
            </div>
          )}
        </div>
        <footer>
          Exact pricing · Version-bound approvals · Reconciled records
        </footer>
      </div>
    );
  if (!data)
    return (
      <main className="loading" aria-live="polite">
        {error || "Opening your desk…"}
        <button onClick={() => load().catch((e) => setError(e.message))}>
          Retry
        </button>
      </main>
    );
  const period = data.periods.find((p) => p.month === month),
    isOpen = period?.state === "open",
    controller = user.role === "controller",
    analyst = user.role === "analyst" || controller;
  const issued = data.invoices.filter((i) => i.state === "issued"),
    total = issued.reduce((n, i) => n + i.total_cents, 0),
    credits = issued.reduce((n, i) => n + i.credited_cents, 0),
    pending = data.approvals.filter((a) => a.state === "pending"),
    breaks = data.breaks.filter((b) => b.state === "open"),
    backlog = data.outbox.filter((o) => o.status !== "confirmed"),
    customerName = (id: string) =>
      data.customers.find((c) => c.id === id)?.name || id;
  const invoices = data.invoices.filter((i) =>
    (i.customer_name + " " + (i.number || "draft"))
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  const closeChecks = [
    [
      "Documents finalized",
      data.invoices.every((i) =>
        ["issued", "void", "cancelled"].includes(i.state),
      ) &&
        data.credits.every((c) =>
          ["issued", "rejected", "cancelled"].includes(c.state),
        ),
    ],
    ["ERP deliveries confirmed", backlog.length === 0],
    ["No open reconciliation breaks", breaks.length === 0],
    [
      "Fresh reconciliation",
      data.recon_runs.some(
        (r) =>
          r.generation === period?.generation &&
          r.findings === 0 &&
          Date.now() - Date.parse(r.created_at) < 300000,
      ),
    ],
    ["Books verified", data.verification.passed],
  ] as const;
  const features = {
    data,
    user,
    controller,
    isOpen,
    busy,
    command,
    special,
    customerName,
    demo,
  };
  function invoiceTable() {
    return (
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Invoice / advertiser</th>
              <th>Status</th>
              <th>Usage</th>
              <th className="right">Amount</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {invoices.map((i) => (
              <tr key={i.id}>
                <td>
                  <button
                    className="text-button"
                    onClick={() => setSelected(i)}
                  >
                    <strong>
                      {i.number
                        ? "INV-" + String(i.number).padStart(5, "0")
                        : (i.state === "cancelled"
                            ? "Cancelled · "
                            : "Draft · ") + i.id.slice(0, 6)}
                    </strong>
                    <small>
                      {i.customer_name}
                      {i.kind === "supplement" ? " · supplemental" : ""}
                    </small>
                  </button>
                </td>
                <td>
                  <Badge state={i.state} />
                </td>
                <td className="muted">
                  {i.impressions.toLocaleString()}{" "}
                  <small>impressions · ${i.rate} CPM</small>
                </td>
                <td className="right amount">
                  {money(i.total_cents)}
                  {i.credited_cents > 0 && (
                    <small>{money(i.credited_cents)} credited</small>
                  )}
                </td>
                <td>
                  <button
                    className="icon-button"
                    aria-label={"Open " + i.customer_name + " invoice"}
                    onClick={() => setSelected(i)}
                  >
                    <ArrowUpRight size={17} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {invoices.length === 0 && (
          <div className="empty">
            <FileText />
            <h3>Your billing cycle starts here</h3>
            <p>
              Generate a frozen preview from one advertiser’s delivered usage
              below.
            </p>
          </div>
        )}
      </div>
    );
  }
  return (
    <div className="app">
      <aside>
        <a className="brand" href="/">
          <span className="brand-icon">
            <BookOpen size={20} />
          </span>
          close desk<span className="brand-dot">.</span>
        </a>
        <div className="workspace">
          <span className="workspace-icon">N</span>
          <div>
            Northline Operations<small>Finance workspace</small>
          </div>
          <ChevronDown size={14} />
        </div>
        <span className="nav-label">WORKSPACE</span>
        <nav>
          {nav.map(([label, Icon]) => (
            <button
              key={label}
              onClick={() => {
                setPage(label);
                setQuery("");
              }}
              className={page === label ? "active" : ""}
            >
              <Icon size={18} />
              {label}
              {label === "Approvals" && pending.length > 0 && (
                <span aria-hidden="true" className="nav-count">
                  {pending.length}
                </span>
              )}
            </button>
          ))}
        </nav>
        <div className="side-bottom">
          <div className="integrity">
            <ShieldCheck size={18} />
            <div>
              {data.verification.passed
                ? "Books are balanced"
                : "Verification failed"}
              <small>Consistent database snapshot</small>
            </div>
            <span
              className={data.verification.passed ? "green-dot" : "red-dot"}
            />
          </div>
          {demo && (
            <label className="persona-select">
              DEMO PERSONA
              <select
                aria-label="Demo persona"
                value={user.id}
                disabled={busy}
                onChange={(e) => login(e.target.value)}
              >
                {personas.map(([id, n, r]) => (
                  <option key={id} value={id}>
                    {n} · {r}
                  </option>
                ))}
              </select>
            </label>
          )}
          <div className="profile">
            <span className="avatar dark">
              {user.name
                .split(" ")
                .map((x) => x[0])
                .join("")}
            </span>
            <div>
              {user.name}
              <small>{user.role}</small>
            </div>
            <button
              aria-label="Sign out"
              className="icon-button"
              disabled={busy}
              onClick={logout}
            >
              <LogOut size={16} />
            </button>
          </div>
        </div>
      </aside>
      <div className="main">
        <header>
          <div>
            <span className="muted">Workspace</span>
            <span className="crumb">/</span>
            <span>{page}</span>
          </div>
          <div className="header-right">
            <span className="synthetic">
              <span />
              Synthetic data
            </span>
            <button
              className="icon-button"
              aria-label="Refresh"
              disabled={busy}
              onClick={() => load().catch((e) => setError(e.message))}
            >
              <RefreshCw size={16} />
            </button>
            <span className="avatar small">{user.name[0]}</span>
          </div>
        </header>
        <main>
          <div className="page-heading">
            <div>
              <div className="eyebrow">BILLING & FINANCIAL OPERATIONS</div>
              <h1>
                {page === "Overview" ? "Every detail, accounted for." : page}
              </h1>
              <p>
                {page === "Overview"
                  ? "A clear view of your billing cycle, from first impression to final close."
                  : page === "Invoices"
                    ? "Frozen previews, deliberate approvals, and immutable issued documents."
                    : page === "Approvals"
                      ? "Review the exact document revision you are signing off on."
                      : page === "Reconciliation"
                        ? "Compare source records. Investigate the differences. Close with evidence."
                        : page === "Journal"
                          ? "An append-only AR subledger. Each posting balances at commit."
                          : "Versioned delivery, bounded retries, and visible recovery."}
              </p>
            </div>
            <label className="period-select">
              <Clock size={16} />
              <select
                aria-label="Billing period"
                value={month}
                onChange={(e) => {
                  loadSequence.current++;
                  setData(null);
                  setMonth(e.target.value);
                  setSelected(null);
                }}
              >
                {data.periods.map((p) => (
                  <option value={p.month} key={p.month}>
                    {new Date(p.month + "T12:00:00Z").toLocaleDateString(
                      "en-US",
                      { month: "long", year: "numeric", timeZone: "UTC" },
                    )}
                  </option>
                ))}
              </select>
              <ChevronDown size={15} />
            </label>
          </div>
          {error && (
            <div role="alert" className="alert">
              <AlertTriangle size={17} />
              {error}
              <button aria-label="Dismiss error" onClick={() => setError("")}>
                <X size={16} />
              </button>
            </div>
          )}
          {notice && (
            <div role="status" className="notice">
              <Check size={17} />
              {notice}
            </div>
          )}
          {(page === "Overview" || page === "Invoices") && (
            <>
              <section className="metrics">
                <article>
                  <span>
                    Issued billings <FileText size={16} />
                  </span>
                  <h2>{money(total)}</h2>
                  <small>
                    {issued.length} issued invoice
                    {issued.length !== 1 ? "s" : ""} this period
                  </small>
                </article>
                <article>
                  <span>
                    Credits applied <ArrowUpRight size={16} />
                  </span>
                  <h2>{money(credits)}</h2>
                  <small>Approved adjustments to billed amounts</small>
                </article>
                <article>
                  <span>
                    Awaiting approval <Clock size={16} />
                  </span>
                  <h2>
                    {pending.length}
                    <em>documents</em>
                  </h2>
                  <small>Every decision is tied to a revision</small>
                </article>
                <article>
                  <span>
                    Open discrepancies <GitCompareArrows size={16} />
                  </span>
                  <h2>
                    {breaks.length}
                    <em>to review</em>
                  </h2>
                  <small>
                    {data.recon_runs.length
                      ? "Latest reconciliation recorded"
                      : "Run your first reconciliation"}
                  </small>
                </article>
              </section>
              <div className="overview-grid">
                <section className="panel invoice-panel">
                  <div className="panel-title">
                    <div>
                      <h2>Invoices</h2>
                      <p>Your billing cycle, in one place.</p>
                    </div>
                    <span className="count">{data.invoices.length} total</span>
                  </div>
                  <div className="table-toolbar">
                    <div className="tabs">
                      <button className="chosen">
                        All invoices <span>{data.invoices.length}</span>
                      </button>
                    </div>
                    <label className="search">
                      <Search size={15} />
                      <input
                        aria-label="Search invoices"
                        placeholder="Find an advertiser…"
                        value={query}
                        onChange={(e) => setQuery(e.target.value)}
                      />
                    </label>
                  </div>
                  {invoiceTable()}
                </section>
                <section className="panel close-panel">
                  <div className="panel-title">
                    <div>
                      <h2>Period close</h2>
                      <p>A checklist backed by live records.</p>
                    </div>
                    <Badge state={period?.state || "open"} />
                  </div>
                  <div className="close-progress">
                    <span>
                      {closeChecks.filter(([, v]) => v).length} of 5 checks
                      complete
                    </span>
                    <progress
                      aria-label="Close checklist completion"
                      max={5}
                      value={closeChecks.filter(([, v]) => v).length}
                    />
                  </div>
                  <ul className="checks">
                    {closeChecks.map(([label, ok]) => (
                      <li key={label}>
                        <span className={ok ? "check done" : "check"}>
                          {ok ? <Check size={13} /> : <span />}
                        </span>
                        {label}
                      </li>
                    ))}
                  </ul>
                  <p className="close-note">
                    <LockKeyhole size={15} />
                    Closing locks financial changes in this period.
                  </p>
                  {controller && (
                    <button
                      className="button primary full"
                      disabled={
                        busy || (isOpen && !closeChecks.every(([, v]) => v))
                      }
                      onClick={() => {
                        const reason = window.prompt(
                          isOpen
                            ? "Reason for closing this period"
                            : "Reason for reopening this period",
                        );
                        if (reason)
                          command({
                            operation: isOpen ? "close" : "reopen",
                            reason,
                          });
                      }}
                    >
                      {isOpen ? "Close period" : "Reopen period"}
                      <ArrowRight size={16} />
                    </button>
                  )}
                </section>
              </div>
              <section className="panel">
                <div className="panel-title">
                  <div>
                    <h2>Delivered usage</h2>
                    <p>
                      Monthly CPM contracts · USD · a frozen source snapshot per
                      preview.
                    </p>
                  </div>
                  <span className="subtle-tag">PostgreSQL fixture source</span>
                </div>
                <div className="customer-grid">
                  {data.customers.map((c) => (
                    <article key={c.id}>
                      <span className="customer-initial">{c.name[0]}</span>
                      <h3>{c.name}</h3>
                      <small>
                        {c.crm_order_ref} · source revision {c.usage_revision}
                      </small>
                      <p>
                        {c.impressions.toLocaleString()}{" "}
                        <span>impressions</span>
                      </p>
                      <div>
                        <span>${c.rate} CPM</span>
                        {analyst && isOpen && (
                          <button
                            disabled={
                              busy ||
                              data.invoices.some(
                                (i) =>
                                  i.customer_id === c.id &&
                                  i.kind === "original" &&
                                  !["void", "cancelled"].includes(i.state),
                              )
                            }
                            className="button small-button"
                            onClick={() =>
                              command({ operation: "preview", customer: c.id })
                            }
                          >
                            <Plus size={14} />
                            Preview
                          </button>
                        )}
                      </div>
                    </article>
                  ))}
                </div>
              </section>
            </>
          )}
          {page === "Approvals" && <Approvals {...features} />}
          {page === "Reconciliation" && <Reconciliation {...features} />}
          {page === "Journal" && <Journal {...features} />}
          {page === "Integrations" && <Integrations {...features} />}
          {page === "Overview" && (
            <section className="panel activity">
              <div className="panel-title">
                <div>
                  <h2>Recent activity</h2>
                  <p>A record of decisions and system changes.</p>
                </div>
                <Activity size={18} />
              </div>
              {data.audit.slice(0, 5).map((a) => (
                <div className="activity-row" key={a.id}>
                  <span className="activity-mark">
                    <Check size={13} />
                  </span>
                  <div>
                    <strong>{a.action.replaceAll("_", " ")}</strong>
                    <small>
                      {a.actor} · audit #{a.id}
                    </small>
                  </div>
                  <time>
                    {new Date(a.at).toLocaleTimeString("en-US", {
                      hour: "numeric",
                      minute: "2-digit",
                    })}
                  </time>
                </div>
              ))}
              {!data.audit.length && (
                <p className="padded muted">
                  Your activity will appear here as the billing cycle
                  progresses.
                </p>
              )}
            </section>
          )}
          <footer className="main-footer">
            <span>Billing Close Desk</span>
            <span>
              USD · Monthly CPM ·{" "}
              {demo ? "Synthetic demonstration" : "Organization workspace"}
            </span>
          </footer>
        </main>
      </div>
      {selected && (
        <div className="modal-backdrop" onClick={() => setSelected(null)}>
          <section
            role="dialog"
            aria-modal="true"
            aria-label="Invoice detail"
            className="modal"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              className="modal-close icon-button"
              aria-label="Close invoice detail"
              onClick={() => setSelected(null)}
            >
              <X />
            </button>
            <span className="eyebrow">
              {selected.number
                ? "INV-" + String(selected.number).padStart(5, "0")
                : "FROZEN PREVIEW"}
            </span>
            <h2>{selected.customer_name}</h2>
            <Badge state={selected.state} />
            <div className="document-total">
              {money(selected.total_cents)}
              <small>
                USD · {selected.impressions.toLocaleString()} impressions at $
                {selected.rate} CPM
              </small>
            </div>
            <dl>
              <dt>Document revision</dt>
              <dd>{selected.revision}</dd>
              <dt>Concurrency version</dt>
              <dd>{selected.version}</dd>
              <dt>Contract</dt>
              <dd>{selected.snapshot.contract_id as string}</dd>
              <dt>Usage source revision</dt>
              <dd>{String(selected.snapshot.usage_revision)}</dd>
              <dt>Credits applied</dt>
              <dd>{money(selected.credited_cents)}</dd>
              <dt>Creator</dt>
              <dd>{selected.created_by}</dd>
            </dl>
            <p className="help">
              {selected.state === "issued" || selected.state === "void"
                ? "Issued content is immutable. Corrections create linked accounting records."
                : "This preview is persisted. Approval signs off on this specific content revision."}
            </p>
            <div className="actions">
              {isOpen &&
                ["draft", "pending", "approved"].includes(selected.state) &&
                (controller || selected.created_by === user.id) && (
                  <button
                    disabled={busy}
                    className="button danger"
                    onClick={() => {
                      const reason = window.prompt(
                        "Reason for discarding this unissued invoice",
                      );
                      if (reason)
                        command({
                          operation: "discard",
                          invoice_id: selected.id,
                          version: selected.version,
                          reason,
                        });
                    }}
                  >
                    Discard draft
                  </button>
                )}
              {isOpen && analyst && selected.state === "draft" && (
                <button
                  disabled={busy}
                  className="button primary"
                  onClick={() =>
                    command({
                      operation: "submit",
                      invoice_id: selected.id,
                      version: selected.version,
                    })
                  }
                >
                  Submit for approval
                  <ArrowRight size={15} />
                </button>
              )}
              {isOpen && controller && selected.state === "approved" && (
                <button
                  disabled={busy}
                  className="button primary"
                  onClick={() =>
                    command({
                      operation: "issue",
                      invoice_id: selected.id,
                      version: selected.version,
                    })
                  }
                >
                  Issue invoice
                </button>
              )}
              {isOpen &&
                analyst &&
                ["draft", "approved"].includes(selected.state) && (
                  <button
                    className="button"
                    onClick={() => {
                      setForm({ kind: "revise", invoice: selected });
                      setValue(selected.rate);
                      setReason("");
                      setSelected(null);
                    }}
                  >
                    Revise rate
                  </button>
                )}
              {isOpen && analyst && selected.state === "issued" && (
                <button
                  className="button"
                  onClick={() => {
                    setForm({ kind: "credit", invoice: selected });
                    setValue("");
                    setReason("");
                    setSelected(null);
                  }}
                >
                  Request credit
                </button>
              )}
              {isOpen &&
                analyst &&
                selected.state === "issued" &&
                selected.kind === "original" && (
                  <button
                    disabled={busy}
                    className="button"
                    onClick={() =>
                      command({
                        operation: "supplement",
                        customer: selected.customer_id,
                        invoice_id: selected.id,
                        reason:
                          "Review supplemental billing for late delivered usage",
                      })
                    }
                  >
                    Check late usage
                  </button>
                )}
              {isOpen &&
                controller &&
                selected.state === "issued" &&
                selected.credited_cents === 0 && (
                  <button
                    disabled={busy}
                    className="button danger"
                    onClick={() => {
                      const reason = window.prompt(
                        "Reason for voiding this invoice",
                      );
                      if (reason)
                        command({
                          operation: "void",
                          invoice_id: selected.id,
                          version: selected.version,
                          reason,
                        });
                    }}
                  >
                    Void invoice
                  </button>
                )}
            </div>
          </section>
        </div>
      )}
      {form && (
        <div className="modal-backdrop">
          <form
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-label={
              form.kind === "credit" ? "Request credit" : "Revise invoice"
            }
            onSubmit={(e) => {
              e.preventDefault();
              try {
                command({
                  operation: form.kind,
                  invoice_id: form.invoice.id,
                  version: form.invoice.version,
                  reason,
                  ...(form.kind === "credit"
                    ? { amount_cents: cents(value) }
                    : { rate: value }),
                });
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            <button
              type="button"
              className="modal-close icon-button"
              aria-label="Close form"
              onClick={() => setForm(null)}
            >
              <X />
            </button>
            <span className="eyebrow">{form.invoice.customer_name}</span>
            <h2>
              {form.kind === "credit"
                ? "Request a credit"
                : "Revise the invoice rate"}
            </h2>
            <p>
              {form.kind === "credit"
                ? "Credits above $250 require two distinct controllers."
                : "Changing this rate invalidates any existing approval."}
            </p>
            <label>
              {form.kind === "credit"
                ? "Credit amount (USD)"
                : "CPM rate (USD)"}
              <input
                required
                inputMode="decimal"
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
            </label>
            <label>
              Reason
              <textarea
                required
                minLength={5}
                maxLength={1000}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <button
              disabled={busy}
              className="button primary full"
              type="submit"
            >
              {form.kind === "credit"
                ? "Create credit request"
                : "Save new revision"}
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
