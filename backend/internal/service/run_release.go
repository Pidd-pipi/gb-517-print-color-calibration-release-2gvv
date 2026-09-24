package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/constants"
	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"github.com/blueship581/print-color-calibration-release/backend/internal/repository"
)

// ProofVerificationError carries every proof that failed the release checklist
// so callers (and the batch detail view) can point at the exact offenders. No
// run or release-decision row is written when this error is returned.
type ProofVerificationError struct {
	Issues []dto.ProofReleaseIssue
}

func (e *ProofVerificationError) Error() string {
	codes := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		codes = append(codes, issue.Code)
	}
	return fmt.Sprintf("校样核对未通过: %s", strings.Join(codes, ", "))
}

// ErrProofVerification marks proof-checklist failures for the HTTP error mapper.
var ErrProofVerification = errors.New("proof verification failed")

func (e *ProofVerificationError) Unwrap() error { return ErrProofVerification }

type RunReleaseService interface {
	ReleaseFromRun(context.Context, uint, dto.RunReleaseRequest, string, string, string) (dto.RunReleaseResult, error)
}

type runReleaseService struct {
	runRepository     repository.PrintRunRepository
	proofRepository   repository.ColorProofRepository
	releaseRepository repository.RunReleaseRepository
}

func NewRunReleaseService(runRepo repository.PrintRunRepository, proofRepo repository.ColorProofRepository, releaseRepo repository.RunReleaseRepository) RunReleaseService {
	return &runReleaseService{runRepository: runRepo, proofRepository: proofRepo, releaseRepository: releaseRepo}
}

func (s *runReleaseService) ReleaseFromRun(ctx context.Context, runID uint, input dto.RunReleaseRequest, actor, role, requestID string) (dto.RunReleaseResult, error) {
	if !canReview(role) {
		return dto.RunReleaseResult{}, ErrForbidden
	}
	codes := normalizeProofCodes(input.ProofCodes)
	if len(codes) == 0 {
		return dto.RunReleaseResult{}, ErrInvalidInput
	}

	run, err := s.runRepository.Get(ctx, runID)
	if err != nil {
		return dto.RunReleaseResult{}, err
	}
	target := string(constants.RunStateReleased)
	if !constants.CanTransition(constants.PrintRunTransitions, run.Status, target) {
		return dto.RunReleaseResult{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, run.Status, target)
	}

	issues, err := s.verifyProofs(ctx, run.Code, codes)
	if err != nil {
		return dto.RunReleaseResult{}, fmt.Errorf("verify proofs for run %s: %w", run.Code, err)
	}
	if len(issues) > 0 {
		// Nothing has been written: the run and the release record stay untouched.
		return dto.RunReleaseResult{ProofIssues: issues}, &ProofVerificationError{Issues: issues}
	}

	previousStatus := run.Status
	run.Status = target
	run.Version = input.ExpectedVersion + 1
	run.UpdatedAt = time.Now().UTC()

	decision := s.buildDecision(run, codes, input, actor)
	runReason := "批次放行：" + strings.Join(codes, ", ")
	decisionReason := "基于批次 " + run.Code + " 的已接收校样放行"
	updatedRun, createdDecision, err := s.releaseRepository.ReleaseRun(ctx, repository.RunReleaseInput{
		Run: &run, PreviousStatus: previousStatus, ExpectedVersion: input.ExpectedVersion,
		Decision: &decision, Actor: actor, RequestID: requestID,
		RunReason:           truncateReason(runReason),
		DecisionReason:      truncateReason(decisionReason),
		RunAuditDetail:      truncateDetail("batch release after proof verification: " + strings.Join(codes, ", ")),
		DecisionAuditDetail: truncateDetail("release decision created from run " + run.Code + " with proofs " + strings.Join(codes, ", ")),
	})
	if err != nil {
		return dto.RunReleaseResult{}, err
	}
	return dto.RunReleaseResult{
		Run:             updatedRun,
		ReleaseDecision: createdDecision,
		VerifiedProofs:  codes,
	}, nil
}

// verifyProofs checks every adopted proof against the run: it must exist, be
// accepted and reference the run as its source.
func (s *runReleaseService) verifyProofs(ctx context.Context, runCode string, codes []string) ([]dto.ProofReleaseIssue, error) {
	proofs, err := s.proofRepository.FindByCodes(ctx, codes)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]model.ColorProof, len(proofs))
	for _, proof := range proofs {
		byCode[proof.Code] = proof
	}
	issues := make([]dto.ProofReleaseIssue, 0)
	for _, code := range codes {
		proof, exists := byCode[code]
		switch {
		case !exists:
			issues = append(issues, dto.ProofReleaseIssue{Code: code, Reason: "校样不存在或尚未登记接收"})
		case proof.Status != string(constants.ColorProofAccepted):
			issues = append(issues, dto.ProofReleaseIssue{Code: code, Reason: "校样未接收，当前状态为 " + proof.Status})
		case strings.ToUpper(strings.TrimSpace(proof.RelatedCode)) != runCode:
			issues = append(issues, dto.ProofReleaseIssue{Code: code, Reason: "来源不一致，校样关联批次为 " + strings.ToUpper(strings.TrimSpace(proof.RelatedCode))})
		}
	}
	return issues, nil
}

func (s *runReleaseService) buildDecision(run model.PrintRun, codes []string, input dto.RunReleaseRequest, actor string) model.ReleaseDecision {
	code := strings.ToUpper(strings.TrimSpace(input.DecisionCode))
	if code == "" {
		code = generateDecisionCode(run.Code)
	}
	evidence := "批次 " + run.Code + " 放行，已核对校样: " + strings.Join(codes, ", ")
	return model.ReleaseDecision{
		BaseModel: model.BaseModel{
			Code: code, Name: run.Name + " 放行记录",
			Status: string(constants.DecisionTypeRelease), Version: 1,
			Description: "由批次详情校样核对后生成的放行记录",
		},
		Facility: run.Facility, Owner: actor, Category: run.Category,
		RiskLevel: run.RiskLevel, MetricValue: run.MetricValue, MetricUnit: run.MetricUnit,
		EffectiveAt: time.Now().UTC(), Evidence: truncateDetail(evidence),
		RelatedCode: run.Code,
	}
}

func normalizeProofCodes(input []string) []string {
	seen := make(map[string]bool)
	codes := make([]string, 0, len(input))
	for _, raw := range input {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes
}

func generateDecisionCode(runCode string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, strings.ToUpper(runCode))
	return fmt.Sprintf("REL-%s-%d", strings.Trim(safe, "-"), time.Now().UTC().Unix())
}

func truncateReason(value string) string {
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func truncateDetail(value string) string {
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}
