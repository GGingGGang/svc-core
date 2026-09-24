package middleware

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

func TestIntrospectionStatus(t *testing.T) {
	status, body := http.StatusOK, `{"active":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected introspection request")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	a := &JWTAuth{introspectURL: srv.URL, client: &http.Client{Timeout: 100 * time.Millisecond}}
	for _, tc := range []struct {
		status         int
		body           string
		active, failed bool
	}{
		{http.StatusOK, `{"active":true}`, true, false},
		{http.StatusOK, `{"active":false}`, false, false},
		{http.StatusUnauthorized, ``, false, false},
		{http.StatusServiceUnavailable, ``, false, true},
		{http.StatusOK, `bad json`, false, true},
	} {
		status, body = tc.status, tc.body
		active, err := a.active(context.Background(), "token")
		if active != tc.active || (err != nil) != tc.failed {
			t.Fatalf("status=%d body=%q: active=%v err=%v", status, body, active, err)
		}
	}
	srv.Close()
	if _, err := a.active(context.Background(), "token"); err == nil {
		t.Fatal("network failure must fail closed")
	}
}

func TestIntrospectionTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"active":true}`))
	}))
	defer srv.Close()
	a := &JWTAuth{introspectURL: srv.URL, client: &http.Client{Timeout: 10 * time.Millisecond}}
	if active, err := a.active(context.Background(), "token"); err == nil || active {
		t.Fatalf("timeout must fail closed: active=%v err=%v", active, err)
	}
}

func TestMiddlewareChecksCurrentAccount(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.FromRaw(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Set(jwk.KeyIDKey, "test-key"); err != nil {
		t.Fatal(err)
	}
	public, err := jwk.PublicKeyOf(key)
	if err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(public); err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusOK)
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(set) })
	mux.HandleFunc("/introspect", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		if status.Load() == http.StatusOK {
			_, _ = w.Write([]byte(`{"active":true}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	a, err := NewJWTAuth(srv.URL+"/jwks", srv.URL+"/introspect", "issuer", "core")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewBuilder().Issuer("issuer").Audience([]string{"core"}).Subject(uuid.NewString()).IssuedAt(time.Now()).Expiration(time.Now().Add(time.Minute)).Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.ES256, key))
	if err != nil {
		t.Fatal(err)
	}
	passed := false
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { passed = true; w.WriteHeader(http.StatusNoContent) }))
	request := func(raw string) int {
		req := httptest.NewRequest(http.MethodGet, "/schedules", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		return resp.Code
	}
	if got := request(string(signed)); got != http.StatusNoContent || !passed {
		t.Fatalf("active status=%d passed=%v", got, passed)
	}
	passed = false
	status.Store(http.StatusUnauthorized)
	if got := request(string(signed)); got != http.StatusUnauthorized || passed {
		t.Fatalf("inactive status=%d passed=%v", got, passed)
	}
	status.Store(http.StatusServiceUnavailable)
	if got := request(string(signed)); got != http.StatusServiceUnavailable || passed {
		t.Fatalf("outage status=%d passed=%v", got, passed)
	}
	before := calls.Load()
	if got := request("invalid"); got != http.StatusUnauthorized || calls.Load() != before {
		t.Fatalf("invalid token status=%d calls=%d", got, calls.Load())
	}
}
