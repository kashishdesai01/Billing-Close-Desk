package tests

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/httpapi"
	"billingdesk/internal/identity"
	"billingdesk/internal/store"
	"billingdesk/internal/worker"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const month = "2026-10-01"

type fakeERP struct {
	mu         sync.Mutex
	docs       map[string]billing.Document
	fail       bool
	acceptFail bool
}

func (f *fakeERP) Put(ctx context.Context, d billing.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("outage")
	}
	if old, ok := f.docs[d.ID]; !ok || old.Sequence < d.Sequence {
		f.docs[d.ID] = d
	}
	if f.acceptFail {
		return errors.New("lost acknowledgement")
	}
	return nil
}
func (f *fakeERP) List(context.Context) ([]billing.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("outage")
	}
	out := []billing.Document{}
	for _, d := range f.docs {
		out = append(out, d)
	}
	return out, nil
}

type fixture struct {
	svc       *billing.Service
	owner, db *pgxpool.Pool
	erp       *fakeERP
	worker    *worker.Worker
}

func setup(t *testing.T) *fixture {
	t.Helper()
	return setupFrom(t, filepath.Join("..", "migrations", "001_init.sql"))
}
func setupFrom(t *testing.T, path string) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	root, e := store.Open(ctx, dsn, false)
	if e != nil {
		t.Fatal(e)
	}
	name := "desk_test_" + store.ID()
	if _, e = root.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	owner, e := store.Open(ctx, u.String(), false)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Migrate(ctx, owner, path); e != nil {
		t.Fatal(e)
	}
	if e = store.Seed(ctx, owner); e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(ctx, u.String(), true)
	if e != nil {
		t.Fatal(e)
	}
	erp := &fakeERP{docs: map[string]billing.Document{}}
	f := &fixture{svc: &billing.Service{DB: db, ERP: erp}, owner: owner, db: db, erp: erp}
	f.worker = &worker.Worker{DB: db, ERP: erp}
	t.Cleanup(func() { db.Close(); owner.Close(); root.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); root.Close() })
	return f
}
func (f *fixture) run(t *testing.T, actor string, c billing.Command) map[string]any {
	t.Helper()
	b, e := f.svc.Command(context.Background(), actor, store.ID(), c)
	if e != nil {
		t.Fatalf("%s as %s: %v", c.Operation, actor, e)
	}
	var out map[string]any
	if e = json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *fixture) invoice(t *testing.T, id string) billing.Invoice {
	t.Helper()
	var v billing.Invoice
	e := f.db.QueryRow(context.Background(), `SELECT id,customer_id,to_char(month,'YYYY-MM-DD'),state,version,revision,total_cents,credited_cents FROM invoices WHERE id=$1`, id).Scan(&v.ID, &v.Customer, &v.Month, &v.State, &v.Version, &v.Revision, &v.Total, &v.Credited)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (f *fixture) draft(t *testing.T, customer string) string {
	return f.run(t, "maya", billing.Command{Operation: "preview", Month: month, Customer: customer})["invoice_id"].(string)
}
func (f *fixture) approve(t *testing.T, id string) {
	v := f.invoice(t, id)
	a := f.run(t, "maya", billing.Command{Operation: "submit", Month: month, InvoiceID: id, Version: v.Version})["approval_id"].(string)
	f.run(t, "alex", billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true})
}
func (f *fixture) issue(t *testing.T, id string) {
	v := f.invoice(t, id)
	f.run(t, "sam", billing.Command{Operation: "issue", Month: month, InvoiceID: id, Version: v.Version})
}
func (f *fixture) verify(t *testing.T) {
	t.Helper()
	issues, e := f.svc.Verify(context.Background())
	if e != nil || len(issues) > 0 {
		t.Fatalf("verify %v %v", issues, e)
	}
}
func TestFullCloseAndReopen(t *testing.T) {
	f := setup(t)
	for _, c := range []string{"northstar", "cedar", "atlas", "kinetic"} {
		id := f.draft(t, c)
		f.approve(t, id)
		f.issue(t, id)
	}
	if _, e := f.worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.run(t, "sam", billing.Command{Operation: "close", Month: month, Reason: "All checks completed"})
	if _, e := f.svc.Command(context.Background(), "maya", store.ID(), billing.Command{Operation: "preview", Month: month, Customer: "northstar"}); e == nil {
		t.Fatal("closed period admitted document")
	}
	f.verify(t)
	f.run(t, "sam", billing.Command{Operation: "reopen", Month: month, Reason: "Controller review requested"})
	f.verify(t)
}
func TestConcurrentIdempotencyAndHashConflict(t *testing.T) {
	f := setup(t)
	key := store.ID()
	c := billing.Command{Operation: "preview", Month: month, Customer: "northstar"}
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	responses := make(chan string, 30)
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := f.svc.Command(context.Background(), "maya", key, c)
			errs <- e
			responses <- string(b)
		}()
	}
	wg.Wait()
	close(errs)
	close(responses)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	one := ""
	for r := range responses {
		if one != "" && one != r {
			t.Fatal("replay differs")
		}
		one = r
	}
	c.Customer = "cedar"
	if _, e := f.svc.Command(context.Background(), "maya", key, c); e == nil {
		t.Fatal("hash mismatch accepted")
	}
	var n int
	f.db.QueryRow(context.Background(), "SELECT count(*) FROM invoices").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestApprovalInvalidatedByRevision(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	v := f.invoice(t, id)
	f.run(t, "maya", billing.Command{Operation: "revise", Month: month, InvoiceID: id, Version: v.Version, Rate: "11.75", Reason: "Negotiated correction"})
	v = f.invoice(t, id)
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "issue", Month: month, InvoiceID: id, Version: v.Version}); e == nil {
		t.Fatal("stale approval issued")
	}
	f.approve(t, id)
	f.issue(t, id)
	f.verify(t)
}
func TestMakerCheckerAndDistinctControllers(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	a := f.run(t, "maya", billing.Command{Operation: "submit", Month: month, InvoiceID: id, Version: v.Version})["approval_id"].(string)
	if _, e := f.db.Exec(context.Background(), `INSERT INTO decisions(id,approval_id,actor,accept,reason) VALUES($1,$2,'maya',true,'')`, store.ID(), a); e == nil {
		t.Fatal("database accepted self approval")
	}
	f.run(t, "alex", billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true})
	f.issue(t, id)
	v = f.invoice(t, id)
	r := f.run(t, "maya", billing.Command{Operation: "credit", Month: month, InvoiceID: id, Version: v.Version, Amount: 30000, Reason: "Service goodwill adjustment"})
	a = r["approval_id"].(string)
	f.run(t, "sam", billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true})
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "apply_credit", Month: month, CreditID: r["credit_id"].(string)}); e == nil {
		t.Fatal("credit before second controller")
	}
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true}); e == nil {
		t.Fatal("duplicate voter")
	}
	f.run(t, "jules", billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true})
	f.run(t, "sam", billing.Command{Operation: "apply_credit", Month: month, CreditID: r["credit_id"].(string)})
	f.verify(t)
}
func TestConcurrentCreditsNeverExceedInvoice(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "cedar")
	f.approve(t, id)
	f.issue(t, id)
	v := f.invoice(t, id)
	ids := []string{}
	for range 2 {
		r := f.run(t, "maya", billing.Command{Operation: "credit", Month: month, InvoiceID: id, Version: v.Version, Amount: v.Total * 3 / 4, Reason: "Competing adjustment"})
		a := r["approval_id"].(string)
		for _, actor := range []string{"sam", "jules"} {
			f.run(t, actor, billing.Command{Operation: "decide", Month: month, ApprovalID: a, Accept: true})
		}
		ids = append(ids, r["credit_id"].(string))
	}
	ch := make(chan error, 2)
	for _, cid := range ids {
		go func() {
			_, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "apply_credit", Month: month, CreditID: cid})
			ch <- e
		}()
	}
	success := 0
	for range 2 {
		if <-ch == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("successful credits", success)
	}
	f.verify(t)
}
func TestLedgerDatabaseBoundaries(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	var entry string
	f.db.QueryRow(context.Background(), "SELECT id FROM journal_entries LIMIT 1").Scan(&entry)
	for name, sql := range map[string]string{"append": `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES('` + entry + `','northstar','1100','D',1),('` + entry + `','northstar','4000','C',1)`, "edit": `UPDATE journal_lines SET amount_cents=2`, "delete": `DELETE FROM journal_entries`, "truncate": `TRUNCATE audit`, "fake_xid": `UPDATE journal_entries SET created_xid=txid_current()`, "issued_edit": `UPDATE invoices SET total_cents=1 WHERE id='` + id + `'`} {
		t.Run(name, func(t *testing.T) {
			if _, e := f.db.Exec(context.Background(), sql); e == nil {
				t.Fatal("database allowed forbidden mutation")
			}
		})
	}
	for _, mode := range []string{"empty", "unbalanced"} {
		t.Run(mode, func(t *testing.T) {
			tx, e := f.db.Begin(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(context.Background())
			eid := store.ID()
			_, e = tx.Exec(context.Background(), `INSERT INTO journal_entries(id,business_key,invoice_id,month,kind) VALUES($1,$2,$3,$4,'credit')`, eid, store.ID(), id, month)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "unbalanced" {
				_, e = tx.Exec(context.Background(), `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES($1,'northstar','1100','D',50)`, eid)
				if e != nil {
					t.Fatal(e)
				}
			}
			if e = tx.Commit(context.Background()); e == nil {
				t.Fatal("invalid journal committed")
			}
		})
	}
	f.verify(t)
}
func TestVoidPreservesARAndNumbers(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	v := f.invoice(t, id)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Incorrect source revision"})
	f.verify(t)
	next := f.draft(t, "northstar")
	f.approve(t, next)
	f.issue(t, next)
	f.verify(t)
}
func TestExpiredWorkerCannotAcknowledge(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	a, e := f.worker.Claim(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	f.owner.Exec(context.Background(), `UPDATE outbox SET locked_until=now()-interval '1 second' WHERE id=$1`, a.ID)
	b, e := f.worker.Claim(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if ok, e := f.worker.Finish(context.Background(), b, nil); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if ok, e := f.worker.Finish(context.Background(), a, errors.New("late failure")); ok || e != nil {
		t.Fatal(ok, e)
	}
	f.verify(t)
}
func TestLostAcknowledgementAndVersionOrdering(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.erp.acceptFail = true
	f.worker.Tick(context.Background())
	if len(f.erp.docs) != 1 {
		t.Fatal("receiver effect missing")
	}
	f.erp.acceptFail = false
	f.owner.Exec(context.Background(), "UPDATE outbox SET next_at=now()")
	f.worker.Tick(context.Background())
	if len(f.erp.docs) != 1 {
		t.Fatal("duplicate receiver identity")
	}
	v := f.invoice(t, id)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Demonstrate stale delivery"})
	f.worker.Tick(context.Background())
	old := f.erp.docs[id]
	old.State = "issued"
	old.Sequence = 1
	f.erp.Put(context.Background(), old)
	if f.erp.docs[id].State != "void" {
		t.Fatal("stale request undid void")
	}
	f.verify(t)
}
func TestReconciliationRecurrenceAndFailedRead(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.worker.Tick(context.Background())
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	original := f.erp.docs[id]
	d := original
	d.Amount++
	f.erp.docs[id] = d
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.erp.fail = true
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "reconcile", Month: month}); e == nil {
		t.Fatal("failed source treated complete")
	}
	var state string
	f.db.QueryRow(context.Background(), `SELECT state FROM breaks WHERE kind='erp_mismatch'`).Scan(&state)
	if state != "open" {
		t.Fatal(state)
	}
	f.erp.fail = false
	f.erp.docs[id] = original
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.erp.docs[id] = d
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	var n int
	f.db.QueryRow(context.Background(), `SELECT occurrences FROM breaks WHERE kind='erp_mismatch'`).Scan(&n)
	if n != 2 {
		t.Fatal("recurrence", n)
	}
}
func TestSupplementIdentityAndSnapshot(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=impressions+1000,revision=revision+1 WHERE customer_id='northstar' AND month=$1`, month)
	r := f.run(t, "maya", billing.Command{Operation: "supplement", Month: month, Customer: "northstar", InvoiceID: id, Reason: "Late delivered usage"})
	sid := r["invoice_id"].(string)
	if _, e := f.svc.Command(context.Background(), "maya", store.ID(), billing.Command{Operation: "supplement", Month: month, Customer: "northstar", InvoiceID: id, Reason: "Late delivered usage"}); e == nil {
		t.Fatal("duplicate supplement")
	}
	f.approve(t, sid)
	f.issue(t, sid)
	v := f.invoice(t, id)
	if v.Total != 1790112 {
		t.Fatal("original recomputed")
	}
	f.verify(t)
}
func TestCloseRequiresCurrentReconciliation(t *testing.T) {
	f := setup(t)
	for _, c := range []string{"northstar", "cedar", "atlas", "kinetic"} {
		id := f.draft(t, c)
		f.approve(t, id)
		f.issue(t, id)
	}
	f.worker.Tick(context.Background())
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	var id string
	f.db.QueryRow(context.Background(), `SELECT id FROM invoices LIMIT 1`).Scan(&id)
	v := f.invoice(t, id)
	f.run(t, "maya", billing.Command{Operation: "credit", Month: month, InvoiceID: id, Version: v.Version, Amount: 100, Reason: "A later adjustment"})
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "close", Month: month, Reason: "Attempt stale close"}); e == nil {
		t.Fatal("stale close succeeded")
	}
}
func TestCloseVersusPreviewSerializes(t *testing.T) {
	f := setup(t)
	for _, c := range []string{"northstar", "cedar", "atlas", "kinetic"} {
		f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=0 WHERE customer_id=$1`, c)
	}
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.owner.Exec(context.Background(), `UPDATE usage_facts SET impressions=1000 WHERE customer_id='northstar'`)
	ch := make(chan error, 2)
	go func() {
		_, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "close", Month: month, Reason: "Race test close"})
		ch <- e
	}()
	go func() {
		_, e := f.svc.Command(context.Background(), "maya", store.ID(), billing.Command{Operation: "preview", Month: month, Customer: "northstar"})
		ch <- e
	}()
	success := 0
	for range 2 {
		if <-ch == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected exactly one operation to succeed: %d", success)
	}
	f.verify(t)
}
func TestStatefulWorkflowInvariants(t *testing.T) {
	f := setup(t)
	rng := rand.New(rand.NewSource(7821))
	for _, customer := range []string{"northstar", "cedar", "atlas", "kinetic"} {
		id := f.draft(t, customer)
		for range 25 {
			v := f.invoice(t, id)
			switch v.State {
			case "draft":
				if rng.Intn(3) == 0 {
					f.run(t, "maya", billing.Command{Operation: "revise", Month: month, InvoiceID: id, Version: v.Version, Rate: fmt.Sprintf("%d.125", 8+rng.Intn(5)), Reason: "Seeded adjustment review"})
				} else {
					f.approve(t, id)
				}
			case "approved":
				f.issue(t, id)
			case "issued":
				if v.Credited == 0 && rng.Intn(8) == 0 {
					f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Seeded void recovery"})
				} else if v.Total-v.Credited > 100 {
					r := f.run(t, "maya", billing.Command{Operation: "credit", Month: month, InvoiceID: id, Version: v.Version, Amount: 100, Reason: "Seeded goodwill adjustment"})
					f.run(t, "alex", billing.Command{Operation: "decide", Month: month, ApprovalID: r["approval_id"].(string), Accept: true})
					f.run(t, "sam", billing.Command{Operation: "apply_credit", Month: month, CreditID: r["credit_id"].(string)})
				}
			case "void":
				id = f.draft(t, customer)
			}
			if rng.Intn(3) == 0 {
				f.worker.Tick(context.Background())
			}
			f.verify(t)
		}
	}
}
func TestAPIAuthOriginAndStrictJSON(t *testing.T) {
	f := setup(t)
	auth := &identity.Auth{DB: f.db, Demo: true}
	api := &httpapi.API{Service: f.svc, Auth: auth, Worker: f.worker, Origin: "http://desk.test"}
	h := api.Handler(http.NotFoundHandler())
	for _, path := range []string{"/api/state?month=" + month, "/api/me"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 401 {
			t.Fatal(rr.Code)
		}
	}
	req := httptest.NewRequest("POST", "/auth/demo", strings.NewReader(`{"user":"sam"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatal("missing origin", rr.Code)
	}
	req = httptest.NewRequest("POST", "/auth/demo", strings.NewReader(`{"user":"sam"}`))
	req.Header.Set("Origin", "http://desk.test")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Fatal(rr.Code)
	}
	cookie := rr.Result().Cookies()[0]
	req = httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var me struct {
		User identity.User `json:"user"`
	}
	json.Unmarshal(rr.Body.Bytes(), &me)
	for _, body := range []string{`{"operation":"close","month":"2026-10-01","unknown":1}`, `{"operation":"close","month":"2026-10-01"} {}`} {
		req = httptest.NewRequest("POST", "/api/commands", strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://desk.test")
		req.Header.Set("X-CSRF-Token", me.User.CSRF)
		req.Header.Set("Idempotency-Key", store.ID())
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 400 {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
	req = httptest.NewRequest("POST", "/api/commands", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "http://desk.test")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatal("missing csrf", rr.Code)
	}
	logout := func(token string) int {
		request := httptest.NewRequest("POST", "/auth/logout", nil)
		request.AddCookie(cookie)
		request.Header.Set("Origin", "http://desk.test")
		request.Header.Set("X-CSRF-Token", token)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response.Code
	}
	if status := logout("wrong-token"); status != 403 {
		t.Fatalf("invalid logout CSRF: %d", status)
	}
	if status := logout(me.User.CSRF); status != 204 {
		t.Fatalf("logout: %d", status)
	}
	if status := logout(me.User.CSRF); status != 401 {
		t.Fatalf("revoked logout session: %d", status)
	}
}
func TestRuntimeCannotAlterIdentityOrUsage(t *testing.T) {
	f := setup(t)
	for _, sql := range []string{`UPDATE users SET role='controller' WHERE id='maya'`, `UPDATE usage_facts SET impressions=1`, `DROP TABLE audit`} {
		if _, e := f.db.Exec(context.Background(), sql); e == nil {
			t.Fatal("runtime privilege escalation", sql)
		}
	}
}
func TestGaplessNumbersAfterFailedIssue(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	v := f.invoice(t, id)
	f.owner.Exec(context.Background(), `CREATE FUNCTION force_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'failpoint'; END $$; CREATE TRIGGER fail_issue BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION force_failure()`)
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "issue", Month: month, InvoiceID: id, Version: v.Version}); e == nil {
		t.Fatal("failure not injected")
	}
	f.owner.Exec(context.Background(), `DROP TRIGGER fail_issue ON outbox`)
	f.issue(t, id)
	var number int
	f.db.QueryRow(context.Background(), `SELECT number FROM invoices WHERE id=$1`, id).Scan(&number)
	if number != 1 {
		t.Fatal(number)
	}
	f.verify(t)
}

func TestEditorsCannotApproveTheirRevision(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	f.run(t, "sam", billing.Command{Operation: "revise", Month: month, InvoiceID: id, Version: v.Version, Rate: "12", Reason: "Controller pricing change"})
	v = f.invoice(t, id)
	r := f.run(t, "maya", billing.Command{Operation: "submit", Month: month, InvoiceID: id, Version: v.Version})
	if _, e := f.svc.Command(context.Background(), "sam", store.ID(), billing.Command{Operation: "decide", Month: month, ApprovalID: r["approval_id"].(string), Accept: true}); e == nil {
		t.Fatal("editor approved own revision")
	}
	f.run(t, "alex", billing.Command{Operation: "decide", Month: month, ApprovalID: r["approval_id"].(string), Accept: true})
	f.issue(t, id)
	f.verify(t)
}
func TestApprovalEvidenceCannotBeForged(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	a := f.run(t, "maya", billing.Command{Operation: "submit", Month: month, InvoiceID: id, Version: v.Version})["approval_id"].(string)
	if _, e := f.db.Exec(context.Background(), `UPDATE approvals SET state='approved' WHERE id=$1`, a); e == nil {
		t.Fatal("approved without decisions")
	}
	if _, e := f.db.Exec(context.Background(), `UPDATE approvals SET content_hash='forged' WHERE id=$1`, a); e == nil {
		t.Fatal("approval hash mutable")
	}
}
func TestCrossPeriodCommandCannotMutateInvoice(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	v := f.invoice(t, id)
	if _, e := f.svc.Command(context.Background(), "maya", store.ID(), billing.Command{Operation: "submit", Month: "2026-09-01", InvoiceID: id, Version: v.Version}); e == nil {
		t.Fatal("period mismatch accepted")
	}
}
func TestImmediateConstraintSwitchCannotUnbalance(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	tx, e := f.db.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	eid := store.ID()
	_, e = tx.Exec(context.Background(), `INSERT INTO journal_entries(id,business_key,invoice_id,month,kind) VALUES($1,$2,$3,$4,'credit')`, eid, store.ID(), id, month)
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(context.Background(), `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES($1,'northstar','1100','D',100),($1,'northstar','4000','C',100)`, eid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `INSERT INTO journal_lines(entry_id,customer_id,account,direction,amount_cents) VALUES($1,'northstar','1100','D',1)`, eid); e == nil {
		t.Fatal("constraint mode switch bypassed balance enforcement")
	}
}
func TestDriftRecoveryAndSubsequentVoidOrdering(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.worker.Tick(context.Background())
	d := f.erp.docs[id]
	d.Amount++
	f.erp.docs[id] = d
	f.run(t, "sam", billing.Command{Operation: "reconcile", Month: month})
	f.run(t, "sam", billing.Command{Operation: "resync", Month: month, DocumentID: id, Reason: "Restore reviewed immutable source"})
	f.worker.Tick(context.Background())
	if f.erp.docs[id].Amount != 1790112 || f.erp.docs[id].Sequence != 2 {
		t.Fatal("drift recovery failed")
	}
	v := f.invoice(t, id)
	f.run(t, "sam", billing.Command{Operation: "void", Month: month, InvoiceID: id, Version: v.Version, Reason: "Void after reviewed resync"})
	f.worker.Tick(context.Background())
	if f.erp.docs[id].State != "void" || f.erp.docs[id].Sequence != 3 {
		t.Fatal("business update lost resync ordering")
	}
	f.verify(t)
}
func TestHostedRuntimeRejectsOwnerCredentials(t *testing.T) {
	f := setup(t)
	if e := store.AssertRuntimeIdentity(context.Background(), f.db); e == nil {
		t.Fatal("owner login accepted as hosted runtime")
	}
}
func TestDeadDeliveryAndExplicitRetry(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	f.erp.fail = true
	for range 8 {
		f.owner.Exec(context.Background(), `UPDATE outbox SET next_at=now()`)
		if _, e := f.worker.Tick(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	var oid, status string
	f.db.QueryRow(context.Background(), `SELECT id,status FROM outbox`).Scan(&oid, &status)
	if status != "dead" {
		t.Fatal(status)
	}
	f.erp.fail = false
	f.run(t, "sam", billing.Command{Operation: "retry", Month: month, OutboxID: oid, Reason: "Receiver recovered after outage"})
	if _, e := f.worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.db.QueryRow(context.Background(), `SELECT status FROM outbox`).Scan(&status)
	if status != "confirmed" {
		t.Fatal(status)
	}
	f.verify(t)
}
func TestIssuedCustomerNameIsFrozen(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	if _, e := f.owner.Exec(context.Background(), `UPDATE customers SET name='Renamed organization' WHERE id='northstar'`); e != nil {
		t.Fatal(e)
	}
	state, e := f.svc.State(context.Background(), month)
	if e != nil {
		t.Fatal(e)
	}
	var invoices []struct {
		Name string `json:"customer_name"`
	}
	if e = json.Unmarshal(state["invoices"].(json.RawMessage), &invoices); e != nil {
		t.Fatal(e)
	}
	if len(invoices) != 1 || invoices[0].Name != "Northstar Media" {
		t.Fatal(invoices)
	}
}
