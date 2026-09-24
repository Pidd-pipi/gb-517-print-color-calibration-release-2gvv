package handler

import (
	"errors"
	"net/http"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/middleware"
	"github.com/blueship581/print-color-calibration-release/backend/internal/repository"
	"github.com/blueship581/print-color-calibration-release/backend/internal/service"
	"github.com/blueship581/print-color-calibration-release/backend/internal/util"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RunReleaseHandler exposes the release workflow started from a batch detail:
// the reviewer adopts proof codes and only a fully verified run is released
// together with its traceable release decision.
type RunReleaseHandler struct {
	service service.RunReleaseService
}

func NewRunReleaseHandler(s service.RunReleaseService) *RunReleaseHandler {
	return &RunReleaseHandler{service: s}
}

func (h *RunReleaseHandler) Register(group *gin.RouterGroup) {
	group.POST("/runs/:id/release", middleware.RequireMinimumRole("reviewer"), h.releaseFromRun)
}

func (h *RunReleaseHandler) releaseFromRun(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.RunReleaseRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.service.ReleaseFromRun(c.Request.Context(), id, input, actorFromContext(c), roleFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleRunReleaseError(c, err, result)
		return
	}
	util.Created(c, result)
}

// handleRunReleaseError maps verification failures to 422 with the offending
// proof list so the batch detail can highlight each problem proof.
func handleRunReleaseError(c *gin.Context, err error, result dto.RunReleaseResult) {
	var verification *service.ProofVerificationError
	if errors.As(err, &verification) {
		util.FailWithDetails(c, http.StatusUnprocessableEntity, "proof_verification_failed", verification.Error(),
			gin.H{"proofIssues": result.ProofIssues})
		return
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		util.Fail(c, http.StatusNotFound, "not_found", "record was not found")
	case errors.Is(err, repository.ErrVersionConflict):
		util.Fail(c, http.StatusConflict, "version_conflict", "record changed; refresh and retry")
	case errors.Is(err, service.ErrInvalidTransition), errors.Is(err, service.ErrInvalidInput):
		util.Fail(c, http.StatusUnprocessableEntity, "business_rule", err.Error())
	case errors.Is(err, service.ErrForbidden):
		util.Fail(c, http.StatusForbidden, "forbidden", err.Error())
	default:
		_ = c.Error(err)
		util.Fail(c, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}
