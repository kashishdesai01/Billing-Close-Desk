package tests

import (
	"billingdesk/internal/identity"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestOIDCSignatureNonceAudienceAndProvisioning(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, e := f.owner.Exec(ctx, `UPDATE users SET email='sam@desk.test',oidc_subject='subject-sam' WHERE id='sam'`)
	if e != nil {
		t.Fatal(e)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test-key"))
	if e != nil {
		t.Fatal(e)
	}
	var issuer, nonce string
	audience, subject := "desk-client", "subject-sam"
	verified := true
	wrongNonce := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if e := r.ParseForm(); e != nil || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(400)
				return
			}
			n := nonce
			if wrongNonce {
				n = "wrong-nonce"
			}
			raw, e := jwt.Signed(signer).Claims(map[string]any{"iss": issuer, "sub": subject, "aud": audience, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": n, "email": "sam@desk.test", "email_verified": verified}).Serialize()
			if e != nil {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque-test-token", "token_type": "Bearer", "expires_in": 60, "id_token": raw})
		default:
			w.WriteHeader(404)
		}
	}))
	defer mock.Close()
	issuer = mock.URL
	provider, e := oidc.NewProvider(ctx, issuer)
	if e != nil {
		t.Fatal(e)
	}
	auth := &identity.Auth{DB: f.db, Provider: provider, OAuth: oauth2.Config{ClientID: "desk-client", ClientSecret: "test-secret", Endpoint: provider.Endpoint(), RedirectURL: "http://desk.test/auth/callback", Scopes: []string{oidc.ScopeOpenID, "email"}}}
	mux := http.NewServeMux()
	auth.Routes(mux)
	attempt := func() (int, *http.Cookie, *http.Request) {
		t.Helper()
		login := httptest.NewRecorder()
		mux.ServeHTTP(login, httptest.NewRequest("GET", "/auth/login", nil))
		if login.Code != 302 {
			t.Fatal(login.Code)
		}
		location, e := url.Parse(login.Header().Get("Location"))
		if e != nil {
			t.Fatal(e)
		}
		nonce = location.Query().Get("nonce")
		if location.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("PKCE missing")
		}
		request := httptest.NewRequest("GET", "/auth/callback?code=example&state="+location.Query().Get("state"), nil)
		request.AddCookie(login.Result().Cookies()[0])
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, request)
		var session *http.Cookie
		for _, c := range result.Result().Cookies() {
			if c.Name == "desk_session" {
				session = c
			}
		}
		return result.Code, session, request
	}
	code, cookie, request := attempt()
	if code != 303 || cookie == nil {
		t.Fatal("valid login", code)
	}
	me := httptest.NewRequest("GET", "/api/me", nil)
	me.AddCookie(cookie)
	if u, e := auth.User(me); e != nil || u.ID != "sam" {
		t.Fatal(u, e)
	}
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, request)
	if replay.Code != 400 {
		t.Fatal("state replay", replay.Code)
	}
	wrongNonce = true
	if code, _, _ := attempt(); code != 401 {
		t.Fatal("wrong nonce", code)
	}
	wrongNonce = false
	audience = "other-client"
	if code, _, _ := attempt(); code != 401 {
		t.Fatal("wrong audience", code)
	}
	audience = "desk-client"
	verified = false
	if code, _, _ := attempt(); code != 403 {
		t.Fatal("unverified email", code)
	}
	verified = true
	subject = "other-subject"
	if code, _, _ := attempt(); code != 403 {
		t.Fatal("unprovisioned subject", code)
	}
}
