package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/GGingGGang/svc-core/internal/service"
)

type Handler struct {
	svc                   *service.Service
	val                   *validator.Validate
	readiness             func(context.Context) error
	followup              func(context.Context) (bool, error)
	internalToken         string
	authAccountStatusURL  string
	notifyInternalBaseURL string
	internalHTTPClient    *http.Client
}

type InternalServiceConfig struct {
	Token                 string
	AuthAccountStatusURL  string
	NotifyInternalBaseURL string
}

func NewHandler(svc *service.Service, readiness func(context.Context) error) *Handler {
	return NewHandlerWithInternal(svc, readiness, InternalServiceConfig{})
}

func NewHandlerWithInternal(svc *service.Service, readiness func(context.Context) error, cfg InternalServiceConfig) *Handler {
	h := &Handler{
		svc: svc, val: validator.New(validator.WithRequiredStructEnabled()), readiness: readiness,
		internalToken: cfg.Token, authAccountStatusURL: cfg.AuthAccountStatusURL,
		notifyInternalBaseURL: cfg.NotifyInternalBaseURL,
		internalHTTPClient:    &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if svc != nil {
		h.followup = svc.FollowupAvailable
	}
	return h
}
