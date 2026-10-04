package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"billingdesk/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ERP is a version-aware simulator port. Its semantics are not a claim about a real ERP.
type ERP interface {
	Put(context.Context, Document) error
	List(context.Context) ([]Document, error)
}
type Document struct {
	ID        string `json:"id"`
	InvoiceID string `json:"invoice_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Customer  string `json:"customer"`
	Month     string `json:"month"`
	Amount    int64  `json:"amount_cents"`
	Sequence  int64  `json:"sequence"`
}
type Service struct {
	DB  *pgxpool.Pool
	ERP ERP
}
type Problem struct {
	Status  int
	Message string
}

func (p *Problem) Error() string      { return p.Message }
func fail(status int, s string) error { return &Problem{status, s} }
func conflict(s string) error         { return fail(http.StatusConflict, s) }

type Command struct {
	DocumentID string `json:"document_id,omitempty"`
	OutboxID   string `json:"outbox_id,omitempty"`
	Operation  string `json:"operation"`
	Month      string `json:"month"`
	Customer   string `json:"customer,omitempty"`
	InvoiceID  string `json:"invoice_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	CreditID   string `json:"credit_id,omitempty"`
	Version    int64  `json:"version,omitempty"`
	Rate       string `json:"rate,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Amount     int64  `json:"amount_cents,omitempty"`
	Accept     bool   `json:"accept,omitempty"`
}
type Invoice struct {
	ID           string          `json:"id"`
	Customer     string          `json:"customer"`
	CustomerName string          `json:"customer_name"`
	ContractID   string          `json:"contract_id"`
	Month        string          `json:"month"`
	Kind         string          `json:"kind"`
	State        string          `json:"state"`
	Revision     int64           `json:"revision"`
	Version      int64           `json:"version"`
	Impressions  int64           `json:"impressions"`
	Rate         string          `json:"rate"`
	Total        int64           `json:"total_cents"`
	Credited     int64           `json:"credited_cents"`
	Number       *int64          `json:"number"`
	CreatedBy    string          `json:"created_by"`
	Makers       []string        `json:"makers"`
	Snapshot     json.RawMessage `json:"snapshot"`
}

const invoiceCols = `i.id,i.customer_id,i.snapshot->>'customer_name',i.contract_id,to_char(i.month,'YYYY-MM-DD'),i.kind,i.state,i.revision,i.version,i.impressions,i.rate::text,i.total_cents,i.credited_cents,i.number,i.created_by,i.snapshot,i.makers`

func scanInvoice(row pgx.Row) (v Invoice, e error) {
	e = row.Scan(&v.ID, &v.Customer, &v.CustomerName, &v.ContractID, &v.Month, &v.Kind, &v.State, &v.Revision, &v.Version, &v.Impressions, &v.Rate, &v.Total, &v.Credited, &v.Number, &v.CreatedBy, &v.Snapshot, &v.Makers)
	return
}
func getInvoice(ctx context.Context, tx pgx.Tx, id string) (Invoice, error) {
	return scanInvoice(tx.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices i JOIN customers c ON c.id=i.customer_id WHERE i.id=$1 FOR UPDATE OF i`, id))
}
func digest(v any) string { sum := sha256.Sum256(store.JSON(v)); return hex.EncodeToString(sum[:]) }
func audit(ctx context.Context, tx pgx.Tx, actor, action, entity string, details any) error {
	_, e := tx.Exec(ctx, `INSERT INTO audit(actor,action,entity,details) VALUES($1,$2,$3,$4)`, actor, action, entity, store.JSON(details))
	return e
}
func (s *Service) Command(ctx context.Context, actor, key string, c Command) (json.RawMessage, error) {
	c.Reason = strings.TrimSpace(c.Reason)
	if len(c.Reason) > 1000 {
		return nil, fail(400, "reason must not exceed 1000 characters")
	}
	if len(key) < 8 || len(key) > 128 {
		return nil, fail(400, "Idempotency-Key must contain 8–128 characters")
	}
	month, e := time.Parse("2006-01-02", c.Month)
	if e != nil || month.Day() != 1 {
		return nil, fail(400, "month must be the first day in YYYY-MM-DD format")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var role string
	if e = tx.QueryRow(ctx, "SELECT role FROM users WHERE id=$1 AND active", actor).Scan(&role); e != nil {
		return nil, fail(403, "inactive or unknown user")
	}
	allowed := false
	switch c.Operation {
	case "preview", "revise", "submit", "supplement", "credit", "discard", "cancel_credit":
		allowed = role == "analyst" || role == "controller"
	case "decide":
		allowed = role == "approver" || role == "controller"
	case "issue", "apply_credit", "void", "reconcile", "retry", "resync":
		allowed = role == "controller"
	case "close", "reopen":
		allowed = role == "controller"
	}
	if !allowed {
		return nil, fail(403, "operation not permitted for this role")
	}
	hash := digest(c)
	tag, e := tx.Exec(ctx, `INSERT INTO idempotency(actor,operation,key,hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, actor, c.Operation, key, hash)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() == 0 {
		var h string
		var b []byte
		if e = tx.QueryRow(ctx, `SELECT hash,response FROM idempotency WHERE actor=$1 AND operation=$2 AND key=$3`, actor, c.Operation, key).Scan(&h, &b); e != nil {
			return nil, e
		}
		if h != hash {
			return nil, fail(422, "idempotency key was used with different content")
		}
		if b == nil {
			return nil, conflict("incomplete command")
		}
		return b, nil
	}
	// One period lock order for every business mutation, reconciliation and close.
	var state string
	var generation int64
	if e = tx.QueryRow(ctx, `SELECT state,generation FROM periods WHERE month=$1 FOR UPDATE`, c.Month).Scan(&state, &generation); e != nil {
		return nil, fail(404, "period does not exist")
	}
	if state != "open" && c.Operation != "reopen" && c.Operation != "reconcile" && c.Operation != "resync" && c.Operation != "retry" {
		return nil, conflict("period is closed")
	}
	var result any
	switch c.Operation {
	case "preview", "supplement":
		result, e = s.preview(ctx, tx, actor, c)
	case "revise", "submit", "issue", "void", "discard":
		result, e = s.invoiceCommand(ctx, tx, actor, c)
	case "credit":
		result, e = s.credit(ctx, tx, actor, c)
	case "decide":
		result, e = s.decide(ctx, tx, actor, c)
	case "cancel_credit":
		result, e = s.cancelCredit(ctx, tx, actor, c)
	case "apply_credit":
		result, e = s.applyCredit(ctx, tx, actor, c)
	case "reconcile":
		result, e = s.reconcile(ctx, tx, actor, c.Month, generation)
	case "close":
		if len(c.Reason) < 5 {
			e = fail(400, "close reason is required")
			break
		}
		var blocked int
		e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoices WHERE month=$1 AND state NOT IN ('issued','void','cancelled'))+(SELECT count(*) FROM credit_requests WHERE month=$1 AND state IN ('pending','approved'))+(SELECT count(*) FROM outbox WHERE month=$1 AND status<>'confirmed')+(SELECT count(*) FROM breaks WHERE month=$1 AND state='open')`, c.Month).Scan(&blocked)
		if e != nil {
			break
		}
		if blocked > 0 {
			e = conflict("close blocked: unfinished documents, sync, or reconciliation breaks")
			break
		}
		var run int64
		e = tx.QueryRow(ctx, `SELECT id FROM recon_runs WHERE month=$1 AND generation=$2 AND findings=0 AND created_at>now()-interval '5 minutes' ORDER BY id DESC LIMIT 1`, c.Month, generation).Scan(&run)
		if e != nil {
			e = conflict("run a fresh clean reconciliation before closing")
			break
		}
		var issues []string
		issues, e = verifyTx(ctx, tx)
		if e != nil {
			break
		}
		if len(issues) > 0 {
			e = conflict("books verification failed")
			break
		}
		_, e = tx.Exec(ctx, `UPDATE periods SET state='closed',closed_at=clock_timestamp(),close_run_id=$2 WHERE month=$1`, c.Month, run)
		result = map[string]any{"closed": true, "recon_run": run}
	case "reopen":
		if state != "closed" {
			e = conflict("period is already open")
			break
		}
		if len(c.Reason) < 5 {
			e = fail(400, "reopening requires a reason")
			break
		}
		_, e = tx.Exec(ctx, `UPDATE periods SET state='open',closed_at=NULL WHERE month=$1`, c.Month)
		result = map[string]any{"reopened": true}
	case "resync":
		if len(c.Reason) < 5 {
			e = fail(400, "resync reason required")
			break
		}
		var d Document
		var status string
		e = tx.QueryRow(ctx, `SELECT payload,status FROM outbox WHERE document_id=$1 AND month=$2 ORDER BY sequence DESC LIMIT 1`, c.DocumentID, c.Month).Scan(&d, &status)
		if e != nil {
			e = fail(404, "delivery document missing")
			break
		}
		if status != "confirmed" {
			e = conflict("finish or retry the existing delivery first")
			break
		}
		e = enqueue(ctx, tx, d)
		result = map[string]any{"queued": true, "document_id": d.ID}
	case "retry":
		if len(c.Reason) < 5 {
			e = fail(400, "retry reason required")
			break
		}
		tag, e = tx.Exec(ctx, `UPDATE outbox SET status='pending',attempts=0,next_at=now(),claim_token=NULL,locked_until=NULL WHERE id=$1 AND month=$2 AND status='dead'`, c.OutboxID, c.Month)
		if e == nil && tag.RowsAffected() == 0 {
			e = conflict("only dead items may be retried")
		}
		result = map[string]any{"queued": true}
	}
	if e != nil {
		return nil, e
	}
	if c.Operation != "reconcile" && c.Operation != "close" {
		if _, e = tx.Exec(ctx, "UPDATE periods SET generation=generation+1 WHERE month=$1", c.Month); e != nil {
			return nil, e
		}
	}
	if e = audit(ctx, tx, actor, c.Operation, c.Month, map[string]any{"command": c, "result": result}); e != nil {
		return nil, e
	}
	b := store.JSON(result)
	if _, e = tx.Exec(ctx, `UPDATE idempotency SET response=$4 WHERE actor=$1 AND operation=$2 AND key=$3`, actor, c.Operation, key, b); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *Service) preview(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	var qty, revision, cap int64
	var contract, rate, customerName string
	e := tx.QueryRow(ctx, `SELECT u.impressions,u.revision,k.id,k.rate::text,x.cap_cents,x.name FROM usage_facts u JOIN customers x ON x.id=u.customer_id JOIN contracts k ON k.customer_id=x.id WHERE u.customer_id=$1 AND u.month=$2 ORDER BY k.version DESC LIMIT 1`, c.Customer, c.Month).Scan(&qty, &revision, &contract, &rate, &cap, &customerName)
	if e != nil {
		return nil, fail(404, "customer usage or contract missing")
	}
	kind, source, original := "original", any(nil), any(nil)
	if c.Operation == "supplement" {
		if len(c.Reason) < 5 {
			return nil, fail(400, "supplement reason required")
		}
		v, e := getInvoice(ctx, tx, c.InvoiceID)
		if e != nil {
			return nil, e
		}
		if v.Month != c.Month || v.Customer != c.Customer || v.State != "issued" || v.Kind != "original" {
			return nil, conflict("supplement requires an issued original in this period")
		}
		var already int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(sum(impressions),0) FROM invoices WHERE original_id=$1 AND state NOT IN ('void','cancelled')`, v.ID).Scan(&already); e != nil {
			return nil, e
		}
		qty -= v.Impressions + already
		if qty <= 0 {
			return nil, conflict("no additional unbilled impressions; decreases require credit review")
		}
		kind = "supplement"
		source = fmt.Sprintf("usage:%s:%s:%d", c.Customer, c.Month, revision)
		original = v.ID
	}
	amount, e := Price(qty, rate)
	if e != nil {
		return nil, fail(400, e.Error())
	}
	if amount <= 0 {
		return nil, conflict("zero-value invoices are unsupported")
	}
	var billed int64
	if e = tx.QueryRow(ctx, `SELECT coalesce(sum(total_cents-credited_cents),0) FROM invoices WHERE customer_id=$1 AND month=$2 AND state NOT IN ('void','cancelled')`, c.Customer, c.Month).Scan(&billed); e != nil {
		return nil, e
	}
	if billed+amount > cap {
		return nil, conflict("monthly contract cap exceeded; revise contract outside this bounded release")
	}
	id := store.ID()
	snap := map[string]any{"customer_name": customerName, "usage_revision": revision, "impressions": qty, "rate": rate, "contract_id": contract, "template_version": "usd-cpm-v1", "currency": "USD", "source": "postgres-fixture", "rounding": "half-away-from-zero per line"}
	_, e = tx.Exec(ctx, `INSERT INTO invoices(id,customer_id,contract_id,month,kind,source_key,original_id,impressions,rate,total_cents,snapshot,created_by,reason,makers) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,ARRAY[$12])`, id, c.Customer, contract, c.Month, kind, source, original, qty, rate, amount, store.JSON(snap), actor, c.Reason)
	return map[string]any{"invoice_id": id, "total_cents": amount, "snapshot": snap}, e
}
func (s *Service) invoiceCommand(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	v, e := getInvoice(ctx, tx, c.InvoiceID)
	if e != nil {
		return nil, fail(404, "invoice not found")
	}
	if v.Month != c.Month {
		return nil, conflict("invoice period mismatch")
	}
	if c.Version != v.Version {
		return nil, conflict("stale version; refresh and review the latest document")
	}
	switch c.Operation {
	case "revise":
		if v.State != "draft" && v.State != "approved" {
			return nil, conflict("only draft or approved invoices may be revised")
		}
		if len(c.Reason) < 5 {
			return nil, fail(400, "revision reason required")
		}
		amount, e := Price(v.Impressions, c.Rate)
		if e != nil || amount <= 0 {
			return nil, fail(400, "invalid rate or zero amount")
		}
		var cap, billed int64
		if e = tx.QueryRow(ctx, `SELECT cap_cents,(SELECT coalesce(sum(total_cents-credited_cents),0) FROM invoices WHERE customer_id=$1 AND month=$2 AND state NOT IN ('void','cancelled') AND id<>$3) FROM customers WHERE id=$1`, v.Customer, c.Month, v.ID).Scan(&cap, &billed); e != nil {
			return nil, e
		}
		if billed+amount > cap {
			return nil, conflict("monthly cap exceeded")
		}
		_, e = tx.Exec(ctx, `UPDATE invoices SET rate=$2::text::numeric,total_cents=$3,revision=revision+1,version=version+1,state='draft',reason=$4,makers=CASE WHEN $5=ANY(makers) THEN makers ELSE array_append(makers,$5) END,snapshot=snapshot||jsonb_build_object('rate',$2::text,'adjustment_reason',$4::text) WHERE id=$1`, v.ID, c.Rate, amount, c.Reason, actor)
		if e != nil {
			return nil, e
		}
	case "submit":
		if v.State != "draft" {
			return nil, conflict("only drafts may be submitted")
		}
		id := store.ID()
		_, e = tx.Exec(ctx, `INSERT INTO approvals(id,document_type,document_id,revision,content_hash,creator,required,makers) VALUES($1,'invoice',$2,$3,$4,$5,1,$6)`, id, v.ID, v.Revision, invoiceHash(v), v.CreatedBy, v.Makers)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE invoices SET state='pending',version=version+1 WHERE id=$1`, v.ID)
		if e != nil {
			return nil, e
		}
		return map[string]any{"approval_id": id}, nil
	case "issue":
		if v.State != "approved" {
			return nil, conflict("invoice requires approval")
		}
		// Hash is bound to financial content; version/state change during approval is excluded.
		var h string
		if e = tx.QueryRow(ctx, `SELECT content_hash FROM approvals WHERE document_type='invoice' AND document_id=$1 AND revision=$2 AND state='approved'`, v.ID, v.Revision).Scan(&h); e != nil {
			return nil, conflict("approval missing")
		}
		if invoiceHash(v) != h {
			return nil, conflict("approved content no longer matches")
		}
		var n int64
		if e = tx.QueryRow(ctx, `UPDATE counters SET value=value+1 WHERE series='invoice' RETURNING value`).Scan(&n); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE invoices SET state='issued',number=$2,issued_at=clock_timestamp(),version=version+1 WHERE id=$1`, v.ID, n); e != nil {
			return nil, e
		}
		if e = post(ctx, tx, v, "issue", v.ID+":issue", v.Total, "1100", "4000"); e != nil {
			return nil, e
		}
		if e = enqueue(ctx, tx, Document{v.ID, v.ID, "invoice", "issued", v.Customer, v.Month, v.Total, 1}); e != nil {
			return nil, e
		}
	case "discard":
		if v.State != "draft" && v.State != "pending" && v.State != "approved" {
			return nil, conflict("only unissued documents can be discarded")
		}
		if len(c.Reason) < 5 {
			return nil, fail(400, "discard reason required")
		}
		if e = canWithdraw(ctx, tx, actor, v.CreatedBy); e != nil {
			return nil, e
		}
		var children int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE original_id=$1 AND state NOT IN ('void','cancelled')`, v.ID).Scan(&children); e != nil {
			return nil, e
		}
		if children > 0 {
			return nil, conflict("resolve active supplemental invoices first")
		}
		if _, e = tx.Exec(ctx, `UPDATE invoices SET state='cancelled',version=version+1 WHERE id=$1`, v.ID); e != nil {
			return nil, e
		}
	case "void":
		if v.State != "issued" || v.Credited != 0 {
			return nil, conflict("void requires an issued invoice without credits")
		}
		if len(c.Reason) < 5 {
			return nil, fail(400, "void reason required")
		}
		var children int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE original_id=$1 AND state NOT IN ('void','cancelled')`, v.ID).Scan(&children); e != nil {
			return nil, e
		}
		if children > 0 {
			return nil, conflict("void or discard active supplemental invoices before voiding their original")
		}
		var pending int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM credit_requests WHERE invoice_id=$1 AND state IN ('pending','approved')`, v.ID).Scan(&pending); e != nil {
			return nil, e
		}
		if pending > 0 {
			return nil, conflict("resolve pending credit requests first")
		}
		_, e = tx.Exec(ctx, `UPDATE invoices SET state='void',version=version+1 WHERE id=$1`, v.ID)
		if e != nil {
			return nil, e
		}
		if e = post(ctx, tx, v, "void", v.ID+":void", v.Total, "4000", "1100"); e != nil {
			return nil, e
		}
		if e = enqueue(ctx, tx, Document{v.ID, v.ID, "invoice", "void", v.Customer, v.Month, v.Total, 2}); e != nil {
			return nil, e
		}
	}
	return map[string]any{"invoice_id": v.ID}, nil
}
func post(ctx context.Context, tx pgx.Tx, v Invoice, kind, key string, amount int64, debit, credit string) error {
	id := store.ID()
	if _, e := tx.Exec(ctx, `INSERT INTO journal_entries(id,business_key,invoice_id,month,kind) VALUES($1,$2,$3,$4,$5)`, id, key, v.ID, v.Month, kind); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES($1,$2,$3,'D',$5),($1,$2,$4,'C',$5)`, id, v.Customer, debit, credit, amount)
	return e
}
func enqueue(ctx context.Context, tx pgx.Tx, d Document) error {
	if e := tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0)+1 FROM outbox WHERE document_id=$1`, d.ID).Scan(&d.Sequence); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO outbox(id,document_id,invoice_id,month,document_type,sequence,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, store.ID(), d.ID, d.InvoiceID, d.Month, d.Type, d.Sequence, store.JSON(d))
	return e
}
func (s *Service) credit(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	v, e := getInvoice(ctx, tx, c.InvoiceID)
	if e != nil {
		return nil, fail(404, "invoice missing")
	}
	if v.Month != c.Month || v.State != "issued" || v.Version != c.Version {
		return nil, conflict("credit requires current issued invoice")
	}
	if c.Amount <= 0 || c.Amount > v.Total-v.Credited || len(c.Reason) < 5 {
		return nil, fail(400, "positive credit within remaining amount and reason required")
	}
	id, aid := store.ID(), store.ID()
	required := 1
	if c.Amount > 25000 {
		required = 2
	}
	var eligible int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE active AND id<>$1 AND (role='controller' OR ($2=1 AND role='approver'))`, actor, required).Scan(&eligible); e != nil {
		return nil, e
	}
	if eligible < required {
		return nil, conflict("not enough eligible independent reviewers for this credit policy")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO credit_requests(id,invoice_id,month,amount_cents,reason,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, v.ID, c.Month, c.Amount, c.Reason, actor); e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO approvals(id,document_type,document_id,revision,content_hash,creator,required) VALUES($1,'credit',$2,1,$3,$4,$5)`, aid, id, digest(map[string]any{"invoice": v.ID, "amount": c.Amount, "reason": c.Reason}), actor, required)
	return map[string]any{"credit_id": id, "approval_id": aid, "required": required}, e
}
func (s *Service) decide(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	var typ, id, state, creator string
	var required int
	var revision int64
	if e := tx.QueryRow(ctx, `SELECT document_type,document_id,state,creator,required,revision FROM approvals WHERE id=$1 FOR UPDATE`, c.ApprovalID).Scan(&typ, &id, &state, &creator, &required, &revision); e != nil {
		return nil, fail(404, "approval missing")
	}
	if state != "pending" {
		return nil, conflict("approval already decided")
	}
	if actor == creator {
		return nil, fail(403, "creator cannot approve their own request")
	}
	if !c.Accept && len(c.Reason) < 5 {
		return nil, fail(400, "rejection reason required")
	}
	var month string
	if typ == "invoice" {
		v, e := getInvoice(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		month = v.Month
		if v.State != "pending" || v.Revision != revision {
			return nil, conflict("approval revision superseded")
		}
	} else {
		if e := tx.QueryRow(ctx, `SELECT to_char(month,'YYYY-MM-DD') FROM credit_requests WHERE id=$1 AND state='pending'`, id).Scan(&month); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return nil, conflict("credit request withdrawn or superseded")
			}
			return nil, e
		}
	}
	if month != c.Month {
		return nil, conflict("approval period mismatch")
	}
	if _, e := tx.Exec(ctx, `INSERT INTO decisions(id,approval_id,actor,accept,reason) VALUES($1,$2,$3,$4,$5)`, store.ID(), c.ApprovalID, actor, c.Accept, c.Reason); e != nil {
		return nil, e
	}
	var count int
	if e := tx.QueryRow(ctx, `SELECT count(*) FROM decisions WHERE approval_id=$1 AND accept`, c.ApprovalID).Scan(&count); e != nil {
		return nil, e
	}
	if !c.Accept {
		state = "rejected"
	} else if count >= required {
		state = "approved"
	}
	if _, e := tx.Exec(ctx, `UPDATE approvals SET state=$2 WHERE id=$1`, c.ApprovalID, state); e != nil {
		return nil, e
	}
	if state != "pending" {
		if typ == "invoice" {
			next := "approved"
			if state == "rejected" {
				next = "draft"
			}
			_, e := tx.Exec(ctx, `UPDATE invoices SET state=$2,version=version+1,revision=revision+CASE WHEN $2='draft' THEN 1 ELSE 0 END WHERE id=$1`, id, next)
			if e != nil {
				return nil, e
			}
		} else {
			_, e := tx.Exec(ctx, `UPDATE credit_requests SET state=$2 WHERE id=$1`, id, state)
			if e != nil {
				return nil, e
			}
		}
	}
	return map[string]any{"state": state, "votes": count}, nil
}
func (s *Service) applyCredit(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	var invoice, state string
	var amount int64
	if e := tx.QueryRow(ctx, `SELECT invoice_id,state,amount_cents FROM credit_requests WHERE id=$1 AND month=$2 FOR UPDATE`, c.CreditID, c.Month).Scan(&invoice, &state, &amount); e != nil {
		return nil, fail(404, "credit missing")
	}
	if state != "approved" {
		return nil, conflict("credit requires completed approval")
	}
	v, e := getInvoice(ctx, tx, invoice)
	if e != nil {
		return nil, e
	}
	tag, e := tx.Exec(ctx, `UPDATE invoices SET credited_cents=credited_cents+$2,version=version+1 WHERE id=$1 AND state='issued' AND credited_cents+$2<=total_cents`, v.ID, amount)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, conflict("credit exceeds remaining invoice balance")
	}
	id := store.ID()
	var n int64
	if e = tx.QueryRow(ctx, `UPDATE counters SET value=value+1 WHERE series='credit' RETURNING value`).Scan(&n); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO credit_memos(id,request_id,invoice_id,number,amount_cents) VALUES($1,$2,$3,$4,$5)`, id, c.CreditID, v.ID, n, amount); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE credit_requests SET state='issued' WHERE id=$1`, c.CreditID); e != nil {
		return nil, e
	}
	if e = post(ctx, tx, v, "credit", c.CreditID+":credit", amount, "4900", "1100"); e != nil {
		return nil, e
	}
	if e = enqueue(ctx, tx, Document{id, v.ID, "credit", "issued", v.Customer, v.Month, amount, 1}); e != nil {
		return nil, e
	}
	return map[string]any{"credit_memo_id": id, "number": n}, nil
}
func IsExpected(e error) bool { var p *Problem; return errors.As(e, &p) }

func invoiceHash(v Invoice) string {
	return digest(map[string]any{"id": v.ID, "customer": v.Customer, "contract": v.ContractID, "month": v.Month, "kind": v.Kind, "revision": v.Revision, "impressions": v.Impressions, "rate": v.Rate, "amount": v.Total, "creator": v.CreatedBy, "snapshot": v.Snapshot, "makers": v.Makers})
}

func canWithdraw(ctx context.Context, tx pgx.Tx, actor, creator string) error {
	if actor == creator {
		return nil
	}
	var controller bool
	if e := tx.QueryRow(ctx, `SELECT role='controller' FROM users WHERE id=$1 AND active`, actor).Scan(&controller); e != nil {
		return e
	}
	if !controller {
		return fail(403, "only the creator or a controller may withdraw this document")
	}
	return nil
}
func (s *Service) cancelCredit(ctx context.Context, tx pgx.Tx, actor string, c Command) (any, error) {
	if len(c.Reason) < 5 {
		return nil, fail(400, "cancellation reason required")
	}
	var creator, state string
	if e := tx.QueryRow(ctx, `SELECT created_by,state FROM credit_requests WHERE id=$1 AND month=$2 FOR UPDATE`, c.CreditID, c.Month).Scan(&creator, &state); e != nil {
		return nil, fail(404, "credit request missing")
	}
	if state != "pending" && state != "approved" {
		return nil, conflict("only unissued credit requests may be cancelled")
	}
	if e := canWithdraw(ctx, tx, actor, creator); e != nil {
		return nil, e
	}
	_, e := tx.Exec(ctx, `UPDATE credit_requests SET state='cancelled' WHERE id=$1`, c.CreditID)
	return map[string]any{"credit_id": c.CreditID, "state": "cancelled"}, e
}
