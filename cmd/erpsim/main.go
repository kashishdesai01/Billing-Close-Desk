// erpsim is a synthetic receiver with explicit version semantics and fault controls.
package main

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/store"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := os.Getenv("ERP_TOKEN")
	if len(token) < 24 {
		slog.Error("ERP_TOKEN requires at least 24 characters")
		os.Exit(1)
	}
	db, e := store.OpenRole(ctx, os.Getenv("DATABASE_URL"), "desk_erp")
	if e != nil {
		slog.Error("database unavailable")
		os.Exit(1)
	}
	defer db.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/records", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			rows, e := db.Query(r.Context(), `SELECT payload FROM erp.records ORDER BY id`)
			if e != nil {
				http.Error(w, "database error", 503)
				return
			}
			defer rows.Close()
			out := []json.RawMessage{}
			for rows.Next() {
				var b []byte
				if rows.Scan(&b) != nil {
					http.Error(w, "database error", 503)
					return
				}
				out = append(out, b)
			}
			if rows.Err() != nil {
				http.Error(w, "database error", 503)
				return
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != "PUT" {
			w.WriteHeader(405)
			return
		}
		var mode string
		if db.QueryRow(r.Context(), `SELECT mode FROM erp.control WHERE id=1`).Scan(&mode) != nil {
			w.WriteHeader(503)
			return
		}
		if mode == "outage" {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "simulated outage", 503)
			return
		}
		var d billing.Document
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&d) != nil || d.ID == "" || d.Sequence < 1 || d.Amount <= 0 || d.Amount > 1_000_000_000_000 || (d.Type != "invoice" && d.Type != "credit") || (d.State != "issued" && d.State != "void") {
			http.Error(w, "invalid document", 400)
			return
		}
		tx, e := db.Begin(r.Context())
		if e != nil {
			w.WriteHeader(503)
			return
		}
		defer tx.Rollback(r.Context()) //nolint:errcheck
		if d.Type == "credit" {
			var exists bool
			if tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM erp.records WHERE id=$1)`, d.InvoiceID).Scan(&exists) != nil || !exists {
				http.Error(w, "dependency not visible", 409)
				return
			}
		}
		b := store.JSON(d)
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])
		tag, e := tx.Exec(r.Context(), `INSERT INTO erp.records(id,sequence,payload,hash) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET sequence=excluded.sequence,payload=excluded.payload,hash=excluded.hash WHERE erp.records.sequence<excluded.sequence`, d.ID, d.Sequence, b, hash)
		if e != nil {
			w.WriteHeader(503)
			return
		}
		if tag.RowsAffected() == 0 {
			var seq int64
			var oldhash string
			if e = tx.QueryRow(r.Context(), `SELECT sequence,hash FROM erp.records WHERE id=$1`, d.ID).Scan(&seq, &oldhash); e != nil {
				w.WriteHeader(503)
				return
			}
			if seq == d.Sequence && oldhash != hash {
				http.Error(w, "same version different payload", 409)
				return
			}
		}
		if e = tx.Commit(r.Context()); e != nil {
			w.WriteHeader(503)
			return
		}
		if mode == "accept_then_fail" {
			http.Error(w, "simulated lost acknowledgement", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /control", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.WriteHeader(401)
			return
		}
		var c struct {
			Mode string `json:"mode"`
			ID   string `json:"id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			w.WriteHeader(400)
			return
		}
		var e error
		switch c.Mode {
		case "normal", "outage", "accept_then_fail":
			_, e = db.Exec(r.Context(), `UPDATE erp.control SET mode=$1 WHERE id=1`, c.Mode)
		case "drift":
			var tag interface{ RowsAffected() int64 }
			tag, e = db.Exec(r.Context(), `UPDATE erp.records SET payload=jsonb_set(payload,'{amount_cents}',to_jsonb((payload->>'amount_cents')::bigint+12500)),hash='external-drift' WHERE id=$1`, c.ID)
			if e == nil && tag.RowsAffected() == 0 {
				w.WriteHeader(404)
				return
			}
		case "repair":
			_, e = db.Exec(r.Context(), `DELETE FROM erp.records WHERE id=$1`, c.ID)
		default:
			w.WriteHeader(400)
			return
		}
		if e != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	addr := os.Getenv("ERP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8091"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}() //nolint:errcheck
	slog.Info("synthetic ERP listening", "address", addr)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		slog.Error("server failed", "error", e)
		os.Exit(1)
	}
}
