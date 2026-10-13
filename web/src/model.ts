export interface User {
  id: string;
  name: string;
  role: string;
  csrf: string;
}
export interface Customer {
  id: string;
  name: string;
  rate: string;
  impressions: number;
  usage_revision: number;
  cap_cents: number;
  crm_order_ref: string;
}
export interface Invoice {
  id: string;
  customer_id: string;
  customer_name: string;
  month: string;
  period: string;
  state: string;
  revision: number;
  version: number;
  impressions: number;
  rate: string;
  total_cents: number;
  credited_cents: number;
  number: number | null;
  created_by: string;
  snapshot: Record<string, unknown>;
  kind: string;
}
export interface Approval {
  revision: number;
  content_hash: string;
  customer_name: string;
  invoice_id: string;
  invoice_revision: number;
  invoice_number: number | null;
  linked_invoice_number: number | null;
  invoice_snapshot: Record<string, unknown> | null;
  credit_reason: string | null;

  id: string;
  document_type: string;
  document_id: string;
  state: string;
  creator: string;
  required: number;
  votes: number;
  voters: string[];
  makers: string[];
  amount_cents: number;
  customer_id: string;
}
export interface Credit {
  id: string;
  invoice_id: string;
  state: string;
  amount_cents: number;
  reason: string;
  created_by: string;
}
export interface Outbox {
  id: string;
  document_id: string;
  document_type: string;
  sequence: number;
  status: string;
  attempts: number;
  last_error: string;
}
export interface Break {
  id: string;
  kind: string;
  subject: string;
  expected: string;
  actual: string;
  state: string;
  occurrences: number;
}
export interface Ledger {
  id: string;
  kind: string;
  account: string;
  direction: string;
  amount_cents: number;
  name: string;
  business_key: string;
}
export interface Period {
  month: string;
  state: string;
  generation: number;
}
export interface State {
  periods: Period[];
  customers: Customer[];
  invoices: Invoice[];
  approvals: Approval[];
  credits: Credit[];
  outbox: Outbox[];
  breaks: Break[];
  audit: { id: number; actor: string; action: string; at: string }[];
  ledger: Ledger[];
  recon_runs: {
    id: number;
    generation: number;
    findings: number;
    created_at: string;
  }[];
  verification: { passed: boolean; issues: string[] };
}
// Only presentation conversion; persisted money and pricing are computed on the server.
export function money(cents: number) {
  if (!Number.isSafeInteger(cents))
    throw new Error("Amount outside exact display range");
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(cents / 100);
}
export function cents(text: string) {
  if (!/^\d{1,10}(\.\d{1,2})?$/.test(text))
    throw new Error("Enter a positive USD amount with at most two decimals");
  const [whole, fraction = ""] = text.split(".");
  const result = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (result <= 0 || !Number.isSafeInteger(result))
    throw new Error("Invalid credit amount");
  return result;
}
