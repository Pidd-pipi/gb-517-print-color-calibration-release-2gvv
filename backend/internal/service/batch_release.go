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

// Proof verification issue codes, mirrored by the detail UI.
const (
	ProofIssueMissing    = "missing"
	ProofIssueNotReady   = "not_accepted"
	ProofIssueWrongRun   = "wrong_run"
	ProofIssueDuplicated = "duplicated"
)

// ProofCheck reports the verification result of one selected proof code.
type ProofCheck struct {
	Code     string `json:"code"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status,omitempty"`
	RunCode  string `json:"runCode,omitempty"`
	Accepted bool   `json:"accepted"`
	Issue    string `json:"issue,omitempty"`
	Message  string `json:"message,omitempty"`
}

// BatchReleaseCheck is returned by preflight and, for failures, embedded in the
// error so the run detail can point at the exact proof that blocks release.
type BatchReleaseCheck struct {
	RunID           uint         `json:"runId"`
	RunCode         string       `json:"runCode"`
	RunStatus       string       `json:"runStatus"`
	TargetStatus    string       `json:"targetStatus"`
	TransitionValid bool         `json:"transitionValid"`
	TransitionIssue string       `json:"transitionIssue,omitempty"`
	Proofs          []ProofCheck `json:"proofs"`
	Ready           bool         `json:"ready"`
}

// BatchReleaseOutcome is returned after a successful checked release.
type BatchReleaseOutcome struct {
	Run      model.PrintRun        `json:"run"`
	Decision model.ReleaseDecision `json:"decision"`
	Proofs   []model.ColorProof    `json:"proofs"`
}

// ErrProofVerification marks a release that must not write anything because a
// selected proof is missing, not accepted, or linked to another batch.
type ErrProofVerification struct {
	Check BatchReleaseCheck
}

func (e *ErrProofVerification) Error() string {
	codes := make([]string, 0)
	for _, proof := range e.Check.Proofs {
		if !proof.Accepted {
			codes = append(codes, proof.Code)
		}
	}
	return fmt.Sprintf("batch release blocked by proofs: %s", strings.Join(codes, ", "))
}

type BatchReleaseService interface {
	Preflight(context.Context, uint, []string) (BatchReleaseCheck, error)
	Release(context.Context, uint, dto.BatchReleaseRequest, string, string, string) (BatchReleaseOutcome, error)
}

type batchReleaseService struct {
	runs     repository.PrintRunRepository
	proofs   repository.ColorProofRepository
	release  repository.BatchReleaseRepository
	security SecurityService
}

func NewBatchReleaseService(
	runs repository.PrintRunRepository,
	proofs repository.ColorProofRepository,
	release repository.BatchReleaseRepository,
	security SecurityService,
) BatchReleaseService {
	return &batchReleaseService{runs: runs, proofs: proofs, release: release, security: security}
}

func (s *batchReleaseService) Preflight(ctx context.Context, runID uint, rawCodes []string) (BatchReleaseCheck, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return BatchReleaseCheck{}, err
	}
	codes, _ := normalizeProofCodes(rawCodes)
	if len(codes) == 0 {
		linked, err := s.proofs.ListByRunCode(ctx, run.Code)
		if err != nil {
			return BatchReleaseCheck{}, err
		}
		codes = make([]string, 0, len(linked))
		for _, proof := range linked {
			codes = append(codes, proof.Code)
		}
	}
	checks, err := s.checkProofs(ctx, run.Code, codes)
	if err != nil {
		return BatchReleaseCheck{}, err
	}
	return buildReleaseCheck(run, codes, checks), nil
}

func (s *batchReleaseService) Release(ctx context.Context, runID uint, input dto.BatchReleaseRequest, actor, role, requestID string) (BatchReleaseOutcome, error) {
	if !canReview(role) {
		return BatchReleaseOutcome{}, ErrForbidden
	}
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return BatchReleaseOutcome{}, err
	}
	codes, duplicates := normalizeProofCodes(input.ProofCodes)
	if len(codes) == 0 {
		check := buildReleaseCheck(run, codes, nil)
		check.Proofs = append(check.Proofs, ProofCheck{
			Accepted: false, Issue: ProofIssueMissing,
			Message: "请至少选择一份本批次采用的校样编号",
		})
		return BatchReleaseOutcome{}, &ErrProofVerification{Check: check}
	}
	if len(duplicates) > 0 {
		// A duplicated selection makes the reviewer's checklist ambiguous;
		// surface the first duplicate alongside the other failed proofs.
		dupCodes := make([]string, 0, len(duplicates))
		for code := range duplicates {
			dupCodes = append(dupCodes, code)
		}
		check := buildReleaseCheck(run, codes, nil)
		check.Proofs = append(check.Proofs, ProofCheck{
			Code: dupCodes[0], Accepted: false, Issue: ProofIssueDuplicated,
			Message: "校样编号重复填写，请逐条核对",
		})
		check.Ready = false
		return BatchReleaseOutcome{}, &ErrProofVerification{Check: check}
	}
	checks, err := s.checkProofs(ctx, run.Code, codes)
	if err != nil {
		return BatchReleaseOutcome{}, err
	}
	check := buildReleaseCheck(run, codes, checks)
	if !check.Ready {
		return BatchReleaseOutcome{}, &ErrProofVerification{Check: check}
	}

	acceptedProofs := make([]model.ColorProof, 0, len(checks))
	for _, proof := range checks {
		acceptedProofs = append(acceptedProofs, *proof.proof)
	}

	target := string(constants.RunStateReleased)
	before := run.Status
	run.Status = target
	run.Version = input.ExpectedVersion + 1
	run.UpdatedAt = time.Now().UTC()

	decision := s.buildDecision(run, codes, input.Reason)
	runReason := fmt.Sprintf("released after proof review: %s", strings.Join(codes, ", "))
	decisionReason := "created from checked batch release: " + input.Reason

	persisted, err := s.release.ReleaseRun(ctx, &run, input.ExpectedVersion, &decision, acceptedProofs, actor, requestID, runReason, decisionReason)
	if err != nil {
		return BatchReleaseOutcome{}, mapReleaseWriteError(err)
	}

	if err := s.security.Audit(ctx, actor, requestID, "transition", "PrintRun", run.ID, before, target, runReason); err != nil {
		return BatchReleaseOutcome{}, fmt.Errorf("persist run release audit: %w", err)
	}
	if err := s.security.Audit(ctx, actor, requestID, "create", "ReleaseDecision", decision.ID, "", decision.Status, decisionReason); err != nil {
		return BatchReleaseOutcome{}, fmt.Errorf("persist decision audit: %w", err)
	}

	return BatchReleaseOutcome{Run: persisted.Run, Decision: persisted.Decision, Proofs: persisted.Proofs}, nil
}

type resolvedProofCheck struct {
	ProofCheck
	proof *model.ColorProof
}

// checkProofs verifies every selected code: it must exist, be accepted by a
// reviewer, and carry this run's code as its source batch.
func (s *batchReleaseService) checkProofs(ctx context.Context, runCode string, codes []string) ([]resolvedProofCheck, error) {
	found, err := s.proofs.ListByCodes(ctx, codes)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]model.ColorProof, len(found))
	for _, proof := range found {
		byCode[proof.Code] = proof
	}
	checks := make([]resolvedProofCheck, 0, len(codes))
	for _, code := range codes {
		check := resolvedProofCheck{ProofCheck: ProofCheck{Code: code, Accepted: true}}
		proof, exists := byCode[code]
		if !exists {
			check.Accepted = false
			check.Issue = ProofIssueMissing
			check.Message = "校样编号不存在或尚未同步"
		} else {
			check.proof = &proof
			check.Name = proof.Name
			check.Status = proof.Status
			check.RunCode = proof.RelatedCode
			switch {
			case proof.Status != model.ColorProofAcceptedStatus:
				check.Accepted = false
				check.Issue = ProofIssueNotReady
				check.Message = "校样尚未被复核员接收"
			case proof.RelatedCode != runCode:
				check.Accepted = false
				check.Issue = ProofIssueWrongRun
				check.Message = fmt.Sprintf("校样来源批次为 %s，与本批次 %s 不一致", proof.RelatedCode, runCode)
			}
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func buildReleaseCheck(run model.PrintRun, codes []string, checks []resolvedProofCheck) BatchReleaseCheck {
	public := make([]ProofCheck, 0, len(checks))
	ready := len(codes) > 0
	for _, check := range checks {
		if !check.Accepted {
			ready = false
		}
		public = append(public, check.ProofCheck)
	}
	check := BatchReleaseCheck{
		RunID: run.ID, RunCode: run.Code, RunStatus: run.Status,
		TargetStatus: string(constants.RunStateReleased), Proofs: public, Ready: ready,
	}
	if !constants.CanTransition(constants.PrintRunTransitions, run.Status, check.TargetStatus) {
		check.TransitionValid = false
		check.TransitionIssue = fmt.Sprintf("批次状态 %s 不允许直接放行", run.Status)
		check.Ready = false
	} else {
		check.TransitionValid = true
	}
	return check
}

func (s *batchReleaseService) buildDecision(run model.PrintRun, codes []string, reason string) model.ReleaseDecision {
	suffix := strings.ToLower(strings.ReplaceAll(run.Code, "_", "-"))
	if len(suffix) > 40 {
		suffix = suffix[:40]
	}
	code := fmt.Sprintf("RD-%s-%d", suffix, time.Now().UTC().UnixNano()%1e7)
	name := run.Name + " 放行记录"
	if len(name) > 160 {
		name = name[:160]
	}
	evidence := fmt.Sprintf("批次 %s 校样核对放行；已接收校样: %s；复核说明: %s",
		run.Code, strings.Join(codes, ", "), strings.TrimSpace(reason))
	if len(evidence) > 2000 {
		evidence = evidence[:2000]
	}
	description := fmt.Sprintf("批次 %s 放行记录，依据 %d 份已接收且来源一致的校样", run.Code, len(codes))
	if len(description) > 1000 {
		description = description[:1000]
	}
	return model.ReleaseDecision{
		BaseModel: model.BaseModel{
			Code: code, Name: name,
			Status: string(constants.DecisionTypeRelease), Version: 1, Description: description,
		},
		Facility: run.Facility, Owner: run.Owner, Category: run.Category, RiskLevel: run.RiskLevel,
		MetricValue: run.MetricValue, MetricUnit: run.MetricUnit, EffectiveAt: time.Now().UTC(),
		Evidence: evidence, RelatedCode: run.Code,
	}
}

// normalizeProofCodes trims, upper-cases and de-duplicates the reviewer's
// selection; the second return value flags codes that appeared more than once.
func normalizeProofCodes(raw []string) ([]string, map[string]bool) {
	seen := make(map[string]bool, len(raw))
	duplicates := make(map[string]bool)
	codes := make([]string, 0, len(raw))
	for _, value := range raw {
		code := strings.ToUpper(strings.TrimSpace(value))
		if code == "" {
			continue
		}
		if seen[code] {
			duplicates[code] = true
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes, duplicates
}

func mapReleaseWriteError(err error) error {
	if errors.Is(err, repository.ErrVersionConflict) {
		return err
	}
	return fmt.Errorf("persist checked batch release: %w", err)
}
