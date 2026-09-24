package router_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blueship581/print-color-calibration-release/backend/internal/database"
	"github.com/blueship581/print-color-calibration-release/backend/internal/router"
	"github.com/gin-gonic/gin"
)

// TestCheckedBatchReleaseFlow covers the reviewer flow launched from a run
// detail view: release only succeeds when every selected proof is accepted and
// linked to that run; otherwise neither run nor release decision is written.
func TestCheckedBatchReleaseFlow(t *testing.T) {
	cfg := testConfig(filepath.Join(t.TempDir(), "gb517-batch.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, redisClient, err := database.Open(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	engine := router.New(cfg, db, redisClient, logger)
	operator := loginToken(t, engine, "operator")
	reviewer := loginToken(t, engine, "reviewer")

	const runCode = "PR-CHK-001"
	status, body := perform(t, engine, http.MethodPost, "/api/runs", operator, "batch-run-create",
		withRelated(recordPayload(runCode, "校样核对放行批次"), runCode))
	run := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	if status != http.StatusCreated {
		t.Fatalf("create run status = %d body=%s", status, body)
	}
	mustTransition(t, engine, "/api/runs", run.ID, run.Version, "printing", "plates mounted", operator, "batch-run-printing")
	mustTransition(t, engine, "/api/runs", run.ID, run.Version+1, "proofing", "proofs captured", operator, "batch-run-proofing")
	runVersion := run.Version + 2

	createAcceptedProof := func(code, related, requestID string) {
		status, body = perform(t, engine, http.MethodPost, "/api/proofs", operator, requestID+"-create",
			withRelated(recordPayload(code, "已接收校样 "+code), related))
		if status != http.StatusCreated {
			t.Fatalf("create proof %s status = %d body=%s", code, status, body)
		}
		proof := decodeData[struct {
			ID      uint `json:"id"`
			Version uint `json:"version"`
		}](t, body)
		mustTransition(t, engine, "/api/proofs", proof.ID, proof.Version, "review", "submitted for review", operator, requestID+"-review")
		mustTransition(t, engine, "/api/proofs", proof.ID, proof.Version+1, "accepted", "readings received", reviewer, requestID+"-accepted")
	}
	createAcceptedProof("CP-OK-1", runCode, "proof-ok1")
	createAcceptedProof("CP-OK-2", runCode, "proof-ok2")
	createAcceptedProof("CP-OTHER", "PR-CHK-002", "proof-other")
	status, body = perform(t, engine, http.MethodPost, "/api/proofs", operator, "proof-pending-create",
		withRelated(recordPayload("CP-PENDING", "待接收校样"), runCode))
	if status != http.StatusCreated {
		t.Fatalf("create CP-PENDING status = %d body=%s", status, body)
	}
	pendingProof := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)
	mustTransition(t, engine, "/api/proofs", pendingProof.ID, pendingProof.Version, "review", "submitted for review", operator, "proof-pending-review")

	// Preflight over proofs currently linked to the run flags the one that has
	// not been received yet.
	status, body = perform(t, engine, http.MethodGet, "/api/runs/"+uintString(run.ID)+"/release-check", reviewer, "preflight", nil)
	preflight := decodeData[struct {
		Ready  bool            `json:"ready"`
		Proofs []proofIssueRow `json:"proofs"`
	}](t, body)
	if status != http.StatusOK || preflight.Ready {
		t.Fatalf("preflight should not be ready: status=%d body=%s", status, body)
	}
	if issueOf(preflight.Proofs, "CP-PENDING") != "not_accepted" {
		t.Fatalf("CP-PENDING must be reported not_accepted, got %+v", preflight.Proofs)
	}

	releasePath := "/api/runs/" + uintString(run.ID) + "/release"
	releasePayload := func(codes []string, version uint) map[string]any {
		return map[string]any{"proofCodes": codes, "expectedVersion": version, "reason": "all colour groups within tolerance"}
	}
	assertBlocked := func(requestID string, codes []string, issue, offending string) {
		t.Helper()
		status, body = perform(t, engine, http.MethodPost, releasePath, reviewer, requestID, releasePayload(codes, runVersion))
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422 body=%s", requestID, status, body)
		}
		failure := decodeFailure(t, body)
		if failure.Error != "proof_verification_failed" || issueOf(failure.Check.Proofs, offending) != issue {
			t.Fatalf("%s: unexpected failure body=%s", requestID, body)
		}
		current := decodeData[struct {
			Status  string `json:"status"`
			Version uint   `json:"version"`
		}](t, mustGet(t, engine, "/api/runs/"+uintString(run.ID), reviewer))
		if current.Status != "proofing" || current.Version != runVersion {
			t.Fatalf("%s: run mutated to %+v, want unchanged proofing v%d", requestID, current, runVersion)
		}
	}
	assertBlocked("release-missing", []string{"CP-OK-1", "CP-GHOST"}, "missing", "CP-GHOST")
	assertBlocked("release-wrong-run", []string{"CP-OK-1", "CP-OTHER"}, "wrong_run", "CP-OTHER")
	assertBlocked("release-pending", []string{"CP-OK-1", "CP-PENDING"}, "not_accepted", "CP-PENDING")

	if decisionsLinkedTo(t, engine, reviewer, runCode) != 0 {
		t.Fatalf("a release decision was written despite failed verifications")
	}

	// The generic run transition endpoint must never bypass proof verification.
	directCode, directBody := perform(t, engine, http.MethodPost, "/api/runs/"+uintString(run.ID)+"/transition", reviewer, "direct-run-release",
		map[string]any{"status": "released", "expectedVersion": runVersion, "reason": "bypass attempt"})
	if directCode != http.StatusUnprocessableEntity {
		t.Fatalf("direct run transition release status = %d, want 422 body=%s", directCode, directBody)
	}

	// Operators stay locked out of the checked release even with valid proofs.
	if status, _ := perform(t, engine, http.MethodPost, releasePath, operator, "operator-checked-release",
		releasePayload([]string{"CP-OK-1", "CP-OK-2"}, runVersion)); status != http.StatusForbidden {
		t.Fatalf("operator checked release status = %d, want 403", status)
	}

	// Reviewer happy path: run and traceable release record are created together.
	status, body = perform(t, engine, http.MethodPost, releasePath, reviewer, "checked-release-ok",
		releasePayload([]string{"CP-OK-1", "CP-OK-2"}, runVersion))
	if status != http.StatusCreated {
		t.Fatalf("checked release status = %d body=%s", status, body)
	}
	outcome := decodeData[struct {
		Run struct {
			Status  string `json:"status"`
			Version uint   `json:"version"`
		} `json:"run"`
		Decision struct {
			Code        string `json:"code"`
			Status      string `json:"status"`
			RelatedCode string `json:"relatedCode"`
			Evidence    string `json:"evidence"`
		} `json:"decision"`
		Proofs []struct {
			Code   string `json:"code"`
			Status string `json:"status"`
		} `json:"proofs"`
	}](t, body)
	if outcome.Run.Status != "released" || outcome.Run.Version != runVersion+1 {
		t.Fatalf("unexpected run outcome %+v", outcome.Run)
	}
	if outcome.Decision.Status != "release" || outcome.Decision.RelatedCode != runCode {
		t.Fatalf("release decision must trace back to %s, got %+v", runCode, outcome.Decision)
	}
	if len(outcome.Proofs) != 2 || !strings.Contains(outcome.Decision.Evidence, "CP-OK-1") || !strings.Contains(outcome.Decision.Evidence, "CP-OK-2") {
		t.Fatalf("decision evidence must name both accepted proofs, got %+v", outcome)
	}

	runDetail := decodeData[struct {
		Revisions []struct {
			Version   uint   `json:"version"`
			Status    string `json:"status"`
			RequestID string `json:"requestId"`
		} `json:"revisions"`
	}](t, mustGet(t, engine, "/api/runs/"+uintString(run.ID), reviewer))
	if len(runDetail.Revisions) != int(runVersion)+1 || runDetail.Revisions[0].Status != "released" || runDetail.Revisions[0].RequestID != "checked-release-ok" {
		t.Fatalf("unexpected run revision chain after checked release: %+v", runDetail.Revisions)
	}
	if decisionsLinkedTo(t, engine, reviewer, runCode) != 1 {
		t.Fatalf("exactly one traceable release decision expected")
	}

	history := decodeData[[]struct {
		Action     string `json:"action"`
		AfterState string `json:"afterState"`
		RequestID  string `json:"requestId"`
	}](t, mustGet(t, engine, "/api/audits/PrintRun/"+uintString(run.ID), reviewer))
	if len(history) == 0 || history[0].AfterState != "released" || history[0].RequestID != "checked-release-ok" {
		t.Fatalf("run audit trail missing checked release: %+v", history)
	}

	// Once released, repeating the flow is rejected without writing again.
	status, body = perform(t, engine, http.MethodPost, releasePath, reviewer, "release-twice",
		releasePayload([]string{"CP-OK-1", "CP-OK-2"}, runVersion+1))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("second release status = %d, want 422 body=%s", status, body)
	}
	failure := decodeFailure(t, body)
	if failure.Check.TransitionValid {
		t.Fatalf("released run must report invalid transition, got %+v", failure.Check)
	}
	if decisionsLinkedTo(t, engine, reviewer, runCode) != 1 {
		t.Fatalf("second release must not create another decision")
	}

	// The standalone release entry keeps working unchanged.
	if status, body = perform(t, engine, http.MethodPost, "/api/release", operator, "standalone-release-create",
		recordPayload("RD-STANDALONE", "独立放行入口")); status != http.StatusCreated {
		t.Fatalf("standalone release create status = %d body=%s", status, body)
	}
}

type proofIssueRow struct {
	Code     string `json:"code"`
	Accepted bool   `json:"accepted"`
	Issue    string `json:"issue"`
}

type failedReleaseBody struct {
	Error string `json:"error"`
	Check struct {
		TransitionValid bool            `json:"transitionValid"`
		Proofs          []proofIssueRow `json:"proofs"`
	} `json:"-"`
}

// decodeFailure reads the 422 envelope {"error":...,"data":{"check":{...}}}.
func decodeFailure(t *testing.T, body []byte) failedReleaseBody {
	t.Helper()
	var envelope struct {
		Error string `json:"error"`
		Data  struct {
			Check json.RawMessage `json:"check"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode failure envelope: %v", err)
	}
	result := failedReleaseBody{Error: envelope.Error}
	if len(envelope.Data.Check) > 0 {
		if err := json.Unmarshal(envelope.Data.Check, &result.Check); err != nil {
			t.Fatalf("decode failure check: %v", err)
		}
	}
	return result
}

func withRelated(payload map[string]any, code string) map[string]any {
	clone := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		clone[key] = value
	}
	clone["relatedCode"] = code
	return clone
}

func mustTransition(t *testing.T, engine *gin.Engine, base string, id, version uint, status, reason, token, requestID string) {
	t.Helper()
	path := base + "/" + uintString(id) + "/transition"
	code, responseBody := perform(t, engine, http.MethodPost, path, token, requestID,
		map[string]any{"status": status, "expectedVersion": version, "reason": reason})
	if code != http.StatusOK {
		t.Fatalf("transition %s -> %s status = %d body=%s", path, status, code, responseBody)
	}
}

func mustGet(t *testing.T, engine *gin.Engine, path, token string) []byte {
	t.Helper()
	status, responseBody := perform(t, engine, http.MethodGet, path, token, "must-get", nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, status, responseBody)
	}
	return responseBody
}

func issueOf(rows []proofIssueRow, code string) string {
	for _, row := range rows {
		if row.Code == code {
			return row.Issue
		}
	}
	return ""
}

func decisionsLinkedTo(t *testing.T, engine *gin.Engine, token, runCode string) int {
	t.Helper()
	decisions := decodeData[[]struct {
		RelatedCode string `json:"relatedCode"`
	}](t, mustGet(t, engine, "/api/release?page=1&pageSize=100", token))
	count := 0
	for _, decision := range decisions {
		if decision.RelatedCode == runCode {
			count++
		}
	}
	return count
}
