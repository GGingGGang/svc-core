// Package middleware holds cross-cutting HTTP middleware.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type contextKey int

const userIDContextKey contextKey = iota

// JWTAuth verifies bearer access tokens against the issuer's JWKS, per
// ../PLAN.md §4.3: signature verification is local (the introspection
// endpoint is retired), and the authenticated user id always comes from the
// token's `sub` claim — never from a request body or path parameter.
type JWTAuth struct {
	cache    *jwk.Cache
	jwksURL  string
	issuer   string
	audience string
}

// NewJWTAuth registers jwksURL with an in-memory jwk.Cache. The cache
// refreshes on the interval implied by the JWKS response's Cache-Control
// header, floored at minRefreshInterval so a misconfigured/short max-age
// cannot turn every request into a refetch. Registration does not fetch the
// JWKS synchronously, so a not-yet-reachable auth service does not block
// this service's own startup (/healthz, /readyz stay up); the first request
// that needs a key triggers the initial fetch.
func NewJWTAuth(jwksURL, issuer, audience string) (*JWTAuth, error) {
	cache := jwk.NewCache(context.Background())
	if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return nil, err
	}
	return &JWTAuth{cache: cache, jwksURL: jwksURL, issuer: issuer, audience: audience}, nil
}

// Middleware rejects requests without a valid Bearer JWT with 401, and
// otherwise injects the token's `sub` (parsed as a UUID) into the request
// context for handlers to read via UserID.
func (a *JWTAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			unauthorized(w)
			return
		}

		token, err := a.verify(r.Context(), []byte(raw))
		if err != nil {
			unauthorized(w)
			return
		}

		id, err := uuid.Parse(token.Subject())
		if err != nil {
			unauthorized(w)
			return
		}

		ctx := context.WithValue(r.Context(), userIDContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// verify resolves the signing key for the token's kid, lazily refreshing the
// JWKS cache once on a miss — key rotation publishes a new kid up to 24h
// ahead of use (../PLAN.md §4.2), so a miss means this instance's cached set
// is merely stale, not that the token is invalid. It then checks the
// signature plus iss/aud/exp/nbf/iat.
func (a *JWTAuth) verify(ctx context.Context, raw []byte) (jwt.Token, error) {
	kid, err := keyID(raw)
	if err != nil {
		return nil, err
	}

	set, err := a.cache.Get(ctx, a.jwksURL)
	if err != nil {
		return nil, err
	}
	if _, ok := set.LookupKeyID(kid); !ok {
		if set, err = a.cache.Refresh(ctx, a.jwksURL); err != nil {
			return nil, err
		}
	}

	return jwt.Parse(raw,
		jwt.WithKeySet(set, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(true),
		jwt.WithIssuer(a.issuer),
		jwt.WithAudience(a.audience),
	)
}

func keyID(raw []byte) (string, error) {
	msg, err := jws.Parse(raw)
	if err != nil {
		return "", err
	}
	sigs := msg.Signatures()
	if len(sigs) == 0 {
		return "", errors.New("jws: token has no signatures")
	}
	return sigs[0].ProtectedHeaders().KeyID(), nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

// UserID returns the authenticated user id placed in the context by the
// active auth middleware. Handlers depend on this function, not on which
// middleware populated it.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDContextKey).(uuid.UUID)
	return id, ok
}
