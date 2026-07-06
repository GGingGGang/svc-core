package api

import (
	"github.com/go-playground/validator/v10"

	"github.com/GGingGGang/svc-core/internal/service"
)

type Handler struct {
	svc *service.Service
	val *validator.Validate
}

func NewHandler(svc *service.Service) *Handler {
	return &Handler{svc: svc, val: validator.New(validator.WithRequiredStructEnabled())}
}
