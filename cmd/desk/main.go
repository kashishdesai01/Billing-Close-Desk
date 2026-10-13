package main

import (
	"billingdesk/internal/billing"
	"billingdesk/internal/httpapi"
	"billingdesk/internal/identity"
	"billingdesk/internal/store"
	"billingdesk/internal/worker"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed all:static
var assets embed.FS

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	mode := flag.String("mode", "serve", "serve, migrate, seed, verify, tick")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, e := store.Open(ctx, os.Getenv("DATABASE_URL"), *mode != "migrate" && *mode != "seed")
	if e != nil {
		slog.Error("database connection could not be established; check configuration")
		os.Exit(1)
	}
	defer db.Close()
	if *mode != "migrate" && *mode != "seed" && os.Getenv("DEMO_PERSONAS") != "true" {
		if e = store.AssertRuntimeIdentity(ctx, db); e != nil {
			slog.Error("unsafe runtime identity", "error", e)
			os.Exit(1)
		}
	}
	erp := &billing.ERPClient{URL: os.Getenv("ERP_URL"), Token: os.Getenv("ERP_TOKEN"), Client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if os.Getenv("ERP_IAM") == "true" {
		erp.Identity, e = idtoken.NewTokenSource(ctx, erp.URL)
		if e != nil {
			slog.Error("ERP IAM identity unavailable")
			os.Exit(1)
		}
	}
	svc := &billing.Service{DB: db, ERP: erp}
	w := &worker.Worker{DB: db, ERP: erp}
	switch *mode {
	case "migrate":
		e = store.Migrate(ctx, db, "migrations/001_init.sql")
		if e == nil {
			e = store.Provision(ctx, db, os.Getenv("RUNTIME_DB_USER"), os.Getenv("ERP_DB_USER"))
		}
	case "seed":
		if os.Getenv("DEMO_PERSONAS") != "true" {
			slog.Error("seed requires explicit DEMO_PERSONAS=true")
			os.Exit(1)
		}
		e = store.Seed(ctx, db)
	case "verify":
		var issues []string
		issues, e = svc.Verify(ctx)
		json.NewEncoder(os.Stdout).Encode(map[string]any{"passed": len(issues) == 0, "issues": issues})
		if len(issues) > 0 {
			os.Exit(1)
		}
	case "tick":
		_, e = w.Tick(ctx)
	case "worker":
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				tickCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
				_, err := w.Tick(tickCtx)
				cancel()
				if err != nil {
					slog.Error("worker tick failed", "error", err)
				}
			}
		}
	case "serve":
		e = serve(ctx, svc, w, erp)
	default:
		slog.Error("invalid mode")
		os.Exit(1)
	}
	if e != nil {
		slog.Error("operation failed", "error", e)
		os.Exit(1)
	}
}
func serve(ctx context.Context, svc *billing.Service, w *worker.Worker, erp *billing.ERPClient) error {
	origin := os.Getenv("APP_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:8090"
	}
	parsed, e := url.Parse(origin)
	if e != nil {
		return e
	}
	demo := os.Getenv("DEMO_PERSONAS") == "true"
	if demo && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && os.Getenv("ALLOW_PUBLIC_SYNTHETIC_DEMO") != "true" {
		slog.Error("public demo requires ALLOW_PUBLIC_SYNTHETIC_DEMO=true")
		os.Exit(1)
	}
	secure := parsed.Scheme == "https"
	if !secure && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		slog.Error("HTTPS required outside localhost")
		os.Exit(1)
	}
	a := &identity.Auth{DB: svc.DB, Demo: demo, Secure: secure}
	if !demo {
		issuer, id, secret := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"), os.Getenv("OIDC_CLIENT_SECRET")
		if issuer == "" || id == "" || secret == "" {
			slog.Error("production mode requires OIDC configuration")
			os.Exit(1)
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		a.Provider, e = oidc.NewProvider(c, issuer)
		if e != nil {
			return e
		}
		a.OAuth = oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: a.Provider.Endpoint(), RedirectURL: origin + "/auth/callback", Scopes: []string{oidc.ScopeOpenID, "email"}}
	}
	if len(erp.Token) < 24 || erp.URL == "" {
		slog.Error("ERP_URL and a strong ERP_TOKEN are required")
		os.Exit(1)
	}
	sub, e := fs.Sub(assets, "static")
	if e != nil {
		return e
	}
	api := &httpapi.API{Service: svc, Auth: a, Worker: w, Origin: origin, WorkerToken: os.Getenv("WORKER_TOKEN"), Sim: erp}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	srv := &http.Server{Addr: addr, Handler: api.Handler(http.FileServer(http.FS(sub))), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}() //nolint:errcheck
	slog.Info("billing close desk listening", "address", addr, "synthetic_demo", demo)
	e = srv.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
