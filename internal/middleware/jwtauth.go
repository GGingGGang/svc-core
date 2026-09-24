// Package middleware holds cross-cutting HTTP middleware.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type contextKey int

const userIDContextKey contextKey = iota

var errJWKSUnavailable = errors.New("jwks unavailable")

// JWTAuth verifies bearer access tokens against the issuer's JWKS, per
// ../PLAN.md §4.3: signature verification is local (the introspection
// endpoint confirms the account is still active), and the user id comes from the
// token's `sub` claim — never from a request body or path parameter.
type JWTAuth struct {
	cache         *jwk.Cache
	jwksURL       string
	introspectURL string
	client        *http.Client
	issuer        string
	audience      string
}

// NewJWTAuth registers jwksURL with an in-memory jwk.Cache. The cache
// refreshes on the interval implied by the JWKS response's Cache-Control
// header, floored at minRefreshInterval so a misconfigured/short max-age
// cannot turn every request into a refetch. Registration does not fetch the
// JWKS synchronously, so a not-yet-reachable auth service does not block
// this service's own startup (/healthz, /readyz stay up); the first request
// that needs a key triggers the initial fetch. introspectURL is the internal
// Auth endpoint queried for current account status after local verification.
func NewJWTAuth(jwksURL, introspectURL, issuer, audience string) (*JWTAuth, error) {
	parsed, err := url.Parse(introspectURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("invalid auth introspection URL")
	}
	cache := jwk.NewCache(context.Background())
	if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return nil, err
	}
	return &JWTAuth{cache: cache, jwksURL: jwksURL, introspectURL: introspectURL, client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, issuer: issuer, audience: audience}, nil
}

// Middleware rejects invalid or inactive Bearer JWTs with 401, fails closed
// with 503 when Auth cannot verify current status, and injects `sub` as a UUID
// context for handlers to read via UserID.
func (a *JWTAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			unauthorized(w)
			return
		}

		verifyCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		token, err := a.verify(verifyCtx, []byte(raw))
		verifyErr := verifyCtx.Err()
		cancel()
		if err != nil {
			if errors.Is(err, errJWKSUnavailable) || verifyErr != nil {
				unavailable(w)
				return
			}
			unauthorized(w)
			return
		}

		id, err := uuid.Parse(token.Subject())
		if err != nil {
			unauthorized(w)
			return
		}
		active, err := a.active(r.Context(), raw)
		if err != nil {
			unavailable(w)
			return
		}
		if !active {
			unauthorized(w)
			return
		}

		ctx := context.WithValue(r.Context(), userIDContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *JWTAuth) active(ctx context.Context, raw string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.introspectURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return false, nil
	case http.StatusOK:
		var body struct {
			Active bool `json:"active"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false, err
		}
		return body.Active, nil
	default:
		return false, errors.New("auth introspection unavailable")
	}
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
		return nil, fmt.Errorf("%w: %v", errJWKSUnavailable, err)
	}
	if _, ok := set.LookupKeyID(kid); !ok {
		if set, err = a.cache.Refresh(ctx, a.jwksURL); err != nil {
			return nil, fmt.Errorf("%w: %v", errJWKSUnavailable, err)
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

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "auth unavailable"})
}

// UserID returns the authenticated user id placed in the context by the
// active auth middleware. Handlers depend on this function, not on which
// middleware populated it.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDContextKey).(uuid.UUID)
	return id, ok
}
