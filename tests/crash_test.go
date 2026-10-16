package tests

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/store"
	"billingdesk/internal/worker"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

// This subprocess entrypoint exists only in the Go test binary, never the app.
func TestCrashDeliveryHelper(t *testing.T) {
	if os.Getenv("DESK_CRASH_HELPER") != "1" {
		return
	}
	db, e := store.Open(context.Background(), os.Getenv("DESK_CRASH_DSN"), true)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(21)
	}
	erp := &billing.ERPClient{URL: os.Getenv("DESK_CRASH_ERP"), Client: http.DefaultClient}
	w := &worker.Worker{DB: db, ERP: erp}
	c, e := w.Claim(context.Background())
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(22)
	}
	if e = erp.Put(context.Background(), c.Document); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(24)
	}
	os.Exit(23)
}
func TestProcessCrashAfterReceiverAcceptance(t *testing.T) {
	f := setup(t)
	id := f.draft(t, "northstar")
	f.approve(t, id)
	f.issue(t, id)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var d billing.Document
		if json.NewDecoder(r.Body).Decode(&d) != nil {
			w.WriteHeader(400)
			return
		}
		f.erp.Put(r.Context(), d)
		w.WriteHeader(204)
	}))
	defer server.Close()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dsn := f.owner.Config().ConnConfig.ConnString()
	cmd := exec.Command(binary, "-test.run=^TestCrashDeliveryHelper$")
	cmd.Env = append(os.Environ(), "DESK_CRASH_HELPER=1", "DESK_CRASH_DSN="+dsn, "DESK_CRASH_ERP="+server.URL)
	out, e := cmd.CombinedOutput()
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("helper did not reach crash boundary: %v %s", e, out)
	}
	if len(f.erp.docs) != 1 {
		t.Fatal("remote effect did not survive child crash")
	}
	if _, e = f.owner.Exec(context.Background(), `UPDATE outbox SET locked_until=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	var status string
	f.db.QueryRow(context.Background(), `SELECT status FROM outbox`).Scan(&status)
	if status != "confirmed" || len(f.erp.docs) != 1 {
		t.Fatal("crash recovery produced incorrect state", status)
	}
	f.verify(t)
}
