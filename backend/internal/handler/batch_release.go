package handler

import (
	"errors"
	"net/http"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/middleware"
	"github.com/blueship581/print-color-calibration-release/backend/internal/service"
	"github.com/blueship581/print-color-calibration-release/backend/internal/util"
	"github.com/gin-gonic/gin"
)

// BatchReleaseHandler exposes the reviewer flow that starts on a 印刷批次
// detail page: preflight the proof checklist, then release with proof codes.
// The standalone /release CRUD entry remains owned by ReleaseDecisionHandler.
type BatchReleaseHandler struct {
	service service.BatchReleaseService
}

func NewBatchReleaseHandler(s service.BatchReleaseService) *BatchReleaseHandler {
	return &BatchReleaseHandler{service: s}
}

func (h *BatchReleaseHandler) Register(group *gin.RouterGroup) {
	resource := group.Group("/runs")
	resource.GET("/:id/release-check", h.preflight)
	resource.POST("/:id/release", middleware.RequireMinimumRole("reviewer"), h.release)
}

func (h *BatchReleaseHandler) preflight(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var query dto.BatchReleasePreflightQuery
	_ = c.ShouldBindQuery(&query)
	check, err := h.service.Preflight(c.Request.Context(), id, query.ProofCodes)
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, check)
}

func (h *BatchReleaseHandler) release(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.BatchReleaseRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	outcome, err := h.service.Release(c.Request.Context(), id, input, actorFromContext(c), roleFromContext(c), requestIDFromContext(c))
	if err != nil {
		var verification *service.ErrProofVerification
		if errors.As(err, &verification) {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "proof_verification_failed",
				"message": verification.Error(),
				"data":    gin.H{"check": verification.Check},
			})
			return
		}
		handleError(c, err)
		return
	}
	util.Created(c, outcome)
}
