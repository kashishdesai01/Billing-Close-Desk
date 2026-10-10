package httpapi

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/identity"
	"billingdesk/internal/store"
	"billingdesk/internal/worker"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type API struct {
	Service             *billing.Service
	Auth                *identity.Auth
	Worker              *worker.Worker
	Origin, WorkerToken string
	Sim                 *billing.ERPClient
}

func problem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": detail})
}
func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		var p *billing.Problem
		var pg *pgconn.PgError
		switch {
		case errors.As(e, &p):
			problem(w, p.Status, p.Message)
		case errors.As(e, &pg) && (pg.Code == "23505" || pg.Code == "23514"):
			problem(w, 409, "constraint rejected this operation; refresh and review current state")
		default:
			slog.Error("request failed", "error", e)
			problem(w, 503, "request could not complete; retry with the same idempotency key")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		problem(w, 400, "invalid JSON request")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		problem(w, 400, "exactly one JSON value is required")
		return false
	}
	return true
}
func (a *API) Handler(static http.Handler) http.Handler {
	m := http.NewServeMux()
	var authMu sync.Mutex
	authWindow := time.Now()
	authCount := 0
	a.Auth.Routes(m)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), time.Second)
		defer c()
		if a.Service.DB.Ping(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})
	m.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		if _, e := a.Auth.User(r); e != nil {
			problem(w, 401, "authentication required")
			return
		}
		ctx, c := context.WithTimeout(r.Context(), 10*time.Second)
		defer c()
		month := r.URL.Query().Get("month")
		parsed, err := time.Parse("2006-01-02", month)
		if err != nil || parsed.Day() != 1 {
			problem(w, 400, "month must be the first day in YYYY-MM-DD format")
			return
		}
		v, e := a.Service.State(ctx, month)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/commands", func(w http.ResponseWriter, r *http.Request) {
		u, e := a.Auth.User(r)
		if e != nil {
			problem(w, 401, "authentication required")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(u.CSRF)) != 1 {
			problem(w, 403, "invalid CSRF token")
			return
		}
		var c billing.Command
		if !decode(w, r, &c) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		v, e := a.Service.Command(ctx, u.ID, r.Header.Get("Idempotency-Key"), c)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/tick", func(w http.ResponseWriter, r *http.Request) {
		if !a.Auth.Demo {
			http.NotFound(w, r)
			return
		}
		u, e := a.Auth.User(r)
		if e != nil || u.Role != "controller" || r.Header.Get("X-CSRF-Token") != u.CSRF {
			problem(w, 403, "controller session required")
			return
		}
		ctx, c := context.WithTimeout(r.Context(), 40*time.Second)
		defer c()
		n, e := a.Worker.Tick(ctx)
		respond(w, map[string]int{"processed": n}, e)
	})
	m.HandleFunc("POST /internal/tick", func(w http.ResponseWriter, r *http.Request) {
		if len(a.WorkerToken) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.WorkerToken)) != 1 {
			problem(w, 401, "worker authentication required")
			return
		}
		ctx, c := context.WithTimeout(r.Context(), 40*time.Second)
		defer c()
		n, e := a.Worker.Tick(ctx)
		respond(w, map[string]int{"processed": n}, e)
	})
	m.HandleFunc("POST /api/simulator", func(w http.ResponseWriter, r *http.Request) {
		if !a.Auth.Demo {
			http.NotFound(w, r)
			return
		}
		u, e := a.Auth.User(r)
		if e != nil || u.Role != "controller" || r.Header.Get("X-CSRF-Token") != u.CSRF {
			problem(w, 403, "controller required")
			return
		}
		var in struct {
			Mode string `json:"mode"`
			ID   string `json:"id"`
		}
		if !decode(w, r, &in) {
			return
		}
		ctx, c := context.WithTimeout(r.Context(), 5*time.Second)
		defer c()
		e = a.Sim.Control(ctx, in.Mode, in.ID)
		respond(w, map[string]any{"mode": in.Mode}, e)
	})
	m.Handle("/", static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := store.ID()
		start := time.Now()
		tracked := &responseStatus{ResponseWriter: w, status: 200}
		w = tracked
		defer func() {
			if r.URL.Path != "/healthz" {
				slog.Info("http_request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "status", tracked.status, "duration_ms", time.Since(start).Milliseconds())
			}
		}()
		if r.URL.Path == "/auth/login" || r.URL.Path == "/auth/demo" {
			authMu.Lock()
			if time.Since(authWindow) > time.Minute {
				authWindow = time.Now()
				authCount = 0
			}
			authCount++
			limited := authCount > 120
			authMu.Unlock()
			if limited {
				w.Header().Set("Retry-After", "60")
				problem(w, 429, "too many login attempts")
				return
			}
		}

		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "request_id", requestID, "stack", string(debug.Stack()))
				problem(w, 500, "internal error")
			}
		}()
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/internal/tick" {
			if strings.TrimRight(r.Header.Get("Origin"), "/") != a.Origin {
				problem(w, 403, "request origin is not allowed")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}

type responseStatus struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *responseStatus) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseStatus) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
