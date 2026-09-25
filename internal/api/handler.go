package api

import (
	"context"

	"github.com/go-playground/validator/v10"

	"github.com/GGingGGang/svc-core/internal/service"
)

type Handler struct {
	svc       *service.Service
	val       *validator.Validate
	readiness func(context.Context) error
	followup  func(context.Context) (bool, error)
}

func NewHandler(svc *service.Service, readiness func(context.Context) error) *Handler {
	h := &Handler{svc: svc, val: validator.New(validator.WithRequiredStructEnabled()), readiness: readiness}
	if svc != nil {
		h.followup = svc.FollowupAvailable
	}
	return h
}
