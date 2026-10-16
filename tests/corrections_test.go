package tests

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/store"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func issuedSupplement(t *testing.T, f *fixture) (string, string) {
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	_, e := f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=impressions+1000,revision=revision+1 WHERE customer_id='northstar' AND month=$1`, month)
	if e != nil {
		t.Fatal(e)
	}
	sid := f.run(t, "maya", billing.Command{Operation: "supplement", Month: month, Customer: "northstar", InvoiceID: id, Reason: "Late source arrivals"})["invoice_id"].(string)
	f.approve(t, sid)
	f.issue(t, sid)
	return id, sid
}
func TestOriginalVoidWithLiveSupplement(t *testing.T) {
	f := setup(t)
	id, sid := issuedSupplement(t, f)
	v := f.invoice(t, id)
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Wrong original invoice"}); e == nil {
		t.Fatal("original voided with active supplement")
	}
	sv := f.invoice(t, sid)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: sid, Version: sv.Version, Reason: "Resolve dependent supplement"})
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Wrong original invoice"})
	next := f.draft(t, "northstar")
	f.approve(t, next)
	f.issue(t, next)
	var qty, billed int64
	if e := f.db.QueryRow(context.Background(), `SELECT impressions,(SELECT sum(impressions) FROM invoices WHERE customer_id='northstar' AND month=$1 AND state='issued') FROM usage_facts WHERE customer_id='northstar' AND month=$1`, month).Scan(&qty, &billed); e != nil {
		t.Fatal(e)
	}
	if qty != billed {
		t.Fatalf("overbilling: source %d billed %d", qty, billed)
	}
	f.verify(t)
}
func TestVoidSupplementRecovery(t *testing.T) {
	f := setup(t)
	id, sid := issuedSupplement(t, f)
	v := f.invoice(t, sid)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: sid, Version: v.Version, Reason: "Wrong supplement rate"})
	_, e := f.svc.Command(context.Background(), "maya", store.ID(), billing.Command{Operation: "supplement", Month: month, Customer: "northstar", InvoiceID: id, Reason: "Recreate corrected supplement"})
	if e != nil {
		t.Fatalf("CONFIRMED RECOVERY BLOCK: %v", e)
	}
}
func TestInsertApprovalEvidenceForgery(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	_, e := f.db.Exec(context.Background(), `INSERT INTO approvals(id,document_type,document_id,revision,content_hash,creator,required,state) VALUES($1,'invoice',$2,1,'forged','maya',1,'approved')`, store.ID(), id)
	if e == nil {
		t.Fatal("CONFIRMED approval INSERT bypass: approved evidence with zero decisions accepted")
	}
}
func TestOutboxPayloadMutable(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	_, e := f.db.Exec(context.Background(), `UPDATE outbox SET payload=jsonb_set(payload,'{amount_cents}','1')`)
	if e == nil {
		t.Fatal("CONFIRMED outbox financial payload mutable by runtime identity")
	}
}
func TestSourceEvidenceHashCompleteness(t *testing.T) {
	f := setup(t)
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	var h1, h2 string
	f.db.QueryRow(context.Background(), `SELECT source_hash FROM recon_runs ORDER BY id DESC LIMIT 1`).Scan(&h1)
	f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=impressions+1000,revision=revision+1 WHERE month=$1 AND customer_id='northstar'`, month)
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.db.QueryRow(context.Background(), `SELECT source_hash FROM recon_runs ORDER BY id DESC LIMIT 1`).Scan(&h2)
	if h1 == h2 {
		t.Fatalf("CONFIRMED different usage snapshots have same recon evidence hash %s", h1)
	}
}
func TestStaleCloseAfterUsageImport(t *testing.T) {
	f := setup(t)
	for _, c := range []string{"northstar", "cedar", "atlas", "kinetic"} {
		id := f.draft(t, c)
		f.approve(t, id)
		f.issue(t, id)
	}
	f.worker.Tick(context.Background())
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	_, e := f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=impressions+1000,revision=revision+1 WHERE month=$1 AND customer_id='northstar'`, month)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "close", Month: month, Reason: "Close after imported restatement"})
	if e == nil {
		t.Fatal("CONFIRMED close accepted obsolete usage reconciliation after fixture import")
	}
}
func TestVerifierCustomerReference(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.owner.Exec(context.Background(), `CREATE FUNCTION wrong_revenue_customer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account='4000' THEN NEW.customer_id='atlas'; END IF; RETURN NEW; END $$; CREATE TRIGGER inject_reference_error BEFORE INSERT ON journal_lines FOR EACH ROW EXECUTE FUNCTION wrong_revenue_customer()`)
	f.issue(t, id)
	issues, e := f.svc.Verify(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(issues) == 0 {
		t.Fatal("CONFIRMED verifier accepts revenue line assigned to wrong invoice customer")
	}
}
func TestVerifierUnbackedCreditNull(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	tx, e := f.db.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	for _, kind := range []string{"issue", "credit"} {
		eid := store.ID()
		_, e = tx.Exec(context.Background(), `INSERT INTO journal_entries(id,business_key,invoice_id,month,kind) VALUES($1,$2,$3,$4,$5)`, eid, store.ID(), id, month, kind)
		if e != nil {
			t.Fatal(e)
		}
		debit, credit := "1100", "4000"
		if kind == "credit" {
			debit, credit = "4900", "1100"
		}
		_, e = tx.Exec(context.Background(), `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES($1,'northstar',$2,'D',$4),($1,'northstar',$3,'C',$4)`, eid, debit, credit, v.Total)
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	issues, e := f.svc.Verify(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(issues) == 0 {
		t.Fatal("CONFIRMED verifier accepts draft issue plus unbacked credit: phantom revenue/contra journal cancels AR, credit amount comparison evaluates NULL")
	}
}
func TestWithdrawnDocumentsReleaseWorkflows(t *testing.T) {
	f := setup(t)
	id, sid := issuedSupplement(t, f)
	sv := f.invoice(t, sid)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: sid, Version: sv.Version, Reason: "Correct supplement preview"})
	draft := f.run(t, "maya", billing.Command{Operation: "supplement", Month: month, Customer: "northstar", InvoiceID: id, Reason: "New corrected supplement"})["invoice_id"].(string)
	dv := f.invoice(t, draft)
	a := f.run(t, "maya", billing.Command{Operation: "submit", Month: month, InvoiceID: draft, Version: dv.Version})["approval_id"].(string)
	dv = f.invoice(t, draft)
	v := f.invoice(t, id)
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Must resolve draft child"}); e == nil {
		t.Fatal("draft child ignored")
	}
	f.run(t, "maya", billing.Command{Operation: "discard", Month: month, InvoiceID: draft, Version: dv.Version, Reason: "Withdraw inaccurate draft"})
	if _, e := f.svc.Command(context.Background(), "alex", store.ID(), billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true}); e == nil {
		t.Fatal("withdrawn document approval accepted")
	}
	r := f.run(t, "maya", billing.Command{Operation: "credit", Month: month, InvoiceID: id, Version: v.Version, Amount: 100, Reason: "Mistaken credit request"})
	f.run(t, "maya", billing.Command{Operation: "cancel_credit", Month: month, CreditID: r["credit_id"].(string), Reason: "Withdraw mistaken request"})
	if _, e := f.svc.Command(context.Background(), "alex", store.ID(), billing.Command{Operation: "decide", Month: month, ApprovalID: r["approval_id"].(string), Accept: true}); e == nil {
		t.Fatal("withdrawn credit approval accepted")
	} else {
		var problem *billing.Problem
		if !errors.As(e, &problem) || problem.Status != 409 {
			t.Fatalf("withdrawn credit should report conflict: %v", e)
		}
	}
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Original now safe to void"})
	f.verify(t)
}
func TestOutboxIdentityAndSequenceAreImmutable(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	for _, sql := range []string{`UPDATE outbox SET sequence=2`, `UPDATE outbox SET document_id='forged'`, `UPDATE outbox SET created_at=now()+interval '1 second'`} {
		if _, e := f.db.Exec(context.Background(), sql); e == nil {
			t.Fatal("outbox envelope modified", sql)
		}
	}
	if _, e := f.worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.verify(t)
}
func TestLatestContractIsShownOnce(t *testing.T) {
	f := setup(t)
	if _, e := f.owner.Exec(context.Background(), `INSERT INTO contracts(id,customer_id,version,rate) VALUES('northstar-v2','northstar',2,13.25)`); e != nil {
		t.Fatal(e)
	}
	state, e := f.svc.State(context.Background(), month)
	if e != nil {
		t.Fatal(e)
	}
	var customers []struct {
		ID   string `json:"id"`
		Rate string `json:"rate"`
	}
	if e = json.Unmarshal(state["customers"].(json.RawMessage), &customers); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, c := range customers {
		if c.ID == "northstar" {
			count++
			if c.Rate != "13.250000" {
				t.Fatal(c)
			}
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	if v.Total != 1921250 {
		t.Fatal(v.Total)
	}
}
func TestBilledWithoutUsageIsNotHidden(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.worker.Tick(context.Background())
	if _, e := f.owner.Exec(context.Background(), `DELETE FROM usage_facts WHERE customer_id='northstar' AND month=$1`, month); e != nil {
		t.Fatal(e)
	}
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	var n int
	if e := f.db.QueryRow(context.Background(), `SELECT count(*) FROM breaks WHERE kind='billed_without_usage' AND subject='northstar' AND state='open'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("missing source record hidden", n, e)
	}
}
func TestExistingDatabaseUpgradeAndChecksum(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "001_init.sql")
	b, e := os.ReadFile(filepath.Join("..", "migrations", "001_init.sql"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	f := setupFrom(t, path)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	if e = store.Migrate(context.Background(), f.owner, filepath.Join("..", "migrations", "001_init.sql")); e != nil {
		t.Fatal(e)
	}
	if e = store.Migrate(context.Background(), f.owner, filepath.Join("..", "migrations", "001_init.sql")); e != nil {
		t.Fatal("repeated upgrade", e)
	}
	var count int
	if e = f.db.QueryRow(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	f.verify(t)
	if e = os.WriteFile(path, append(b, []byte("\n-- altered applied migration\n")...), 0600); e != nil {
		t.Fatal(e)
	}
	if e = store.Migrate(context.Background(), f.owner, path); e == nil {
		t.Fatal("changed applied migration accepted")
	}
}
