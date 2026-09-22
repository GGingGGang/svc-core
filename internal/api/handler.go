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
}

func NewHandler(svc *service.Service, readiness func(context.Context) error) *Handler {
	return &Handler{svc: svc, val: validator.New(validator.WithRequiredStructEnabled()), readiness: readiness}
}
