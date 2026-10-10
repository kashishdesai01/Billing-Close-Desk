// Package identity provides opaque database sessions and OIDC authorization-code login.
package identity

import (
	"billingdesk/internal/store"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
	"net/http"
	"time"
)

type Auth struct {
	DB       *pgxpool.Pool
	Demo     bool
	Secure   bool
	Provider *oidc.Provider
	OAuth    oauth2.Config
}
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	CSRF string `json:"csrf"`
}

func hash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func (a *Auth) User(r *http.Request) (User, error) {
	var u User
	c, e := r.Cookie("desk_session")
	if e != nil {
		return u, e
	}
	e = a.DB.QueryRow(r.Context(), `SELECT u.id,u.name,u.role,s.csrf FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=$1 AND s.expires>now() AND u.active`, hash(c.Value)).Scan(&u.ID, &u.Name, &u.Role, &u.CSRF)
	return u, e
}
func (a *Auth) session(w http.ResponseWriter, r *http.Request, user string) error {
	token, csrf := store.ID()+store.ID(), store.ID()
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if old, e := r.Cookie("desk_session"); e == nil {
		if _, e = tx.Exec(r.Context(), `DELETE FROM sessions WHERE hash=$1`, hash(old.Value)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(r.Context(), `DELETE FROM sessions WHERE expires<now()`); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO sessions(hash,user_id,csrf,expires) VALUES($1,$2,$3,now()+interval '8 hours')`, hash(token), user, csrf); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "desk_session", Value: token, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	return nil
}
func (a *Auth) Routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"demo": a.Demo})
	})
	m.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		u, e := a.User(r)
		if e != nil {
			http.Error(w, "authentication required", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"user": u, "demo": a.Demo})
	})
	m.HandleFunc("POST /auth/demo", func(w http.ResponseWriter, r *http.Request) {
		if !a.Demo {
			http.NotFound(w, r)
			return
		}
		var in struct {
			User string `json:"user"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		switch in.User {
		case "maya", "alex", "sam", "jules", "viewer":
		default:
			w.WriteHeader(400)
			return
		}
		if a.session(w, r, in.User) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	m.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		u, e := a.User(r)
		if e != nil {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-CSRF-Token") != u.CSRF {
			w.WriteHeader(403)
			return
		}
		c, _ := r.Cookie("desk_session")
		if _, e = a.DB.Exec(r.Context(), `DELETE FROM sessions WHERE hash=$1`, hash(c.Value)); e != nil {
			w.WriteHeader(503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "desk_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
	})
	m.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		if a.Provider == nil {
			http.Error(w, "OIDC not configured", 503)
			return
		}
		if _, e := a.DB.Exec(r.Context(), `DELETE FROM oidc_states WHERE expires<now()`); e != nil {
			w.WriteHeader(503)
			return
		}
		state, nonce, verifier := store.ID(), store.ID(), oauth2.GenerateVerifier()
		_, e := a.DB.Exec(r.Context(), `INSERT INTO oidc_states(hash,nonce,verifier,expires) VALUES($1,$2,$3,now()+interval '5 minutes')`, hash(state), nonce, verifier)
		if e != nil {
			w.WriteHeader(503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "desk_oidc", Value: state, Path: "/auth/callback", Secure: a.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
		http.Redirect(w, r, a.OAuth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), 302)
	})
	m.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if a.Provider == nil {
			w.WriteHeader(503)
			return
		}
		c, e := r.Cookie("desk_oidc")
		if e != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
			http.Error(w, "invalid login state", 400)
			return
		}
		var nonce, verifier string
		e = a.DB.QueryRow(r.Context(), `DELETE FROM oidc_states WHERE hash=$1 AND expires>now() RETURNING nonce,verifier`, hash(c.Value)).Scan(&nonce, &verifier)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		token, e := a.OAuth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
		if e != nil {
			w.WriteHeader(401)
			return
		}
		raw, ok := token.Extra("id_token").(string)
		if !ok {
			w.WriteHeader(401)
			return
		}
		id, e := a.Provider.Verifier(&oidc.Config{ClientID: a.OAuth.ClientID}).Verify(ctx, raw)
		if e != nil || id.Nonce != nonce {
			w.WriteHeader(401)
			return
		}
		var claims struct {
			Email    string `json:"email"`
			Verified bool   `json:"email_verified"`
		}
		if id.Claims(&claims) != nil || !claims.Verified {
			w.WriteHeader(403)
			return
		}
		var user string
		e = a.DB.QueryRow(ctx, `SELECT id FROM users WHERE email=$1 AND oidc_subject=$2 AND active`, claims.Email, id.Subject).Scan(&user)
		if e != nil {
			http.Error(w, "identity is not provisioned", 403)
			return
		}
		if a.session(w, r, user) != nil {
			w.WriteHeader(503)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "desk_oidc", Path: "/auth/callback", MaxAge: -1, Secure: a.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", 303)
	})
}
