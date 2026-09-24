package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

type proofVerificationDetails struct {
	ProofIssues []struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"proofIssues"`
}

type releaseFailure struct {
	Error   string                   `json:"error"`
	Message string                   `json:"message"`
	Details proofVerificationDetails `json:"details"`
}

// TestRunReleaseFromDetail walks the release workflow initiated from a print
// run detail: adopted proofs must exist, be accepted and reference the run.
// Verification failures must leave both the run and release decisions untouched.
func TestRunReleaseFromDetail(t *testing.T) {
	cfg := testConfig(setupTestDSN(t, "run-release"))
	engine, cancel := setupTestRouter(t, cfg)
	defer cancel()

	tokens := map[string]string{}
	for _, role := range []string{"viewer", "operator", "reviewer", "admin"} {
		tokens[role] = loginToken(t, engine, role)
	}

	// Seeded PR-003 is in proofing with accepted proofs CP-003/CP-004; CP-005
	// is accepted but sourced from PR-002.
	pr003 := findSeededRun(t, engine, tokens["reviewer"], "PR-003")
	releasePath := fmt.Sprintf("/api/runs/%d/release", pr003.ID)

	// Operator must not drive the reviewer-only release entrypoint.
	payload := map[string]any{"expectedVersion": pr003.Version, "proofCodes": []string{"CP-003", "CP-004"}, "reason": "proofs verified on press"}
	if status, body := perform(t, engine, http.MethodPost, releasePath, tokens["operator"], "run-release-operator", payload); status != http.StatusForbidden {
		t.Fatalf("operator run release status = %d, want 403 body=%s", status, body)
	}

	// Missing (never received) proof: 422 with the offending code, no writes.
	missing := map[string]any{"expectedVersion": pr003.Version, "proofCodes": []string{"CP-003", "CP-404"}, "reason": "proofs verified on press"}
	status, body := perform(t, engine, http.MethodPost, releasePath, tokens["reviewer"], "run-release-missing", missing)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("missing proof release status = %d, want 422 body=%s", status, body)
	}
	failure := decodeFailure(t, body)
	if failure.Error != "proof_verification_failed" || len(failure.Details.ProofIssues) != 1 || failure.Details.ProofIssues[0].Code != "CP-404" {
		t.Fatalf("unexpected missing-proof failure: %+v", failure)
	}

	// Source mismatch: CP-005 is accepted but belongs to PR-002.
	mismatch := map[string]any{"expectedVersion": pr003.Version, "proofCodes": []string{"CP-003", "CP-005"}, "reason": "proofs verified on press"}
	status, body = perform(t, engine, http.MethodPost, releasePath, tokens["reviewer"], "run-release-mismatch", mismatch)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched proof release status = %d, want 422 body=%s", status, body)
	}
	failure = decodeFailure(t, body)
	if len(failure.Details.ProofIssues) != 1 || failure.Details.ProofIssues[0].Code != "CP-005" {
		t.Fatalf("unexpected mismatch failure: %+v", failure.Details.ProofIssues)
	}

	// Unaccepted proof CP-002 (review state) blocks release as well.
	unaccepted := map[string]any{"expectedVersion": pr003.Version, "proofCodes": []string{"CP-002"}, "reason": "proofs verified on press"}
	if status, body = perform(t, engine, http.MethodPost, releasePath, tokens["reviewer"], "run-release-unaccepted", unaccepted); status != http.StatusUnprocessableEntity {
		t.Fatalf("unaccepted proof release status = %d, want 422 body=%s", status, body)
	}

	// After failed attempts the run is untouched and still proofing at v1.
	pr003 = findSeededRun(t, engine, tokens["reviewer"], "PR-003")
	if pr003.Status != "proofing" || pr003.Version != 1 {
		t.Fatalf("run mutated after failed verification: %+v", pr003)
	}
	if total := countReleaseDecisions(t, engine, tokens["reviewer"]); total != 3 {
		t.Fatalf("release decisions after failed verification = %d, want still 3 seeded rows", total)
	}

	// Happy path: both accepted, correctly sourced proofs release atomically.
	status, body = perform(t, engine, http.MethodPost, releasePath, tokens["reviewer"], "run-release-ok", payload)
	if status != http.StatusCreated {
		t.Fatalf("successful run release status = %d body=%s", status, body)
	}
	result := decodeData[struct {
		Run struct {
			ID      uint   `json:"id"`
			Status  string `json:"status"`
			Version uint   `json:"version"`
		} `json:"run"`
		ReleaseDecision struct {
			Code        string `json:"code"`
			Status      string `json:"status"`
			RelatedCode string `json:"relatedCode"`
		} `json:"releaseDecision"`
		VerifiedProofs []string `json:"verifiedProofs"`
	}](t, body)
	if result.Run.Status != "released" || result.Run.Version != 2 {
		t.Fatalf("unexpected released run: %+v", result.Run)
	}
	if result.ReleaseDecision.Status != "release" || result.ReleaseDecision.RelatedCode != "PR-003" {
		t.Fatalf("release decision not traced to run: %+v", result.ReleaseDecision)
	}
	if len(result.VerifiedProofs) != 2 {
		t.Fatalf("verified proofs = %v", result.VerifiedProofs)
	}

	// The released run now exposes a second immutable revision and can no longer
	// be released again.
	_, detailBody := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/runs/%d", pr003.ID), tokens["reviewer"], "run-release-read", nil)
	detail := decodeData[struct {
		Status    string `json:"status"`
		Version   uint   `json:"version"`
		Revisions []struct {
			Version   uint   `json:"version"`
			Status    string `json:"status"`
			RequestID string `json:"requestId"`
		} `json:"revisions"`
	}](t, detailBody)
	if detail.Status != "released" || detail.Version != 2 || len(detail.Revisions) != 2 || detail.Revisions[0].RequestID != "run-release-ok" {
		t.Fatalf("unexpected released run detail: %+v", detail)
	}
	retry := map[string]any{"expectedVersion": detail.Version, "proofCodes": []string{"CP-003"}, "reason": "duplicate release attempt"}
	if status, body := perform(t, engine, http.MethodPost, releasePath, tokens["reviewer"], "run-release-duplicate", retry); status != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate release status = %d, want 422 body=%s", status, body)
	}

	// Stale optimistic-lock version is rejected without creating a decision.
	// A fresh proofing run is bumped proofing -> hold -> proofing by a reviewer
	// while the release request still carries the earlier version.
	operatorRun := createRunAndProof(t, engine, tokens)
	before := countReleaseDecisions(t, engine, tokens["reviewer"])
	bumpPath := fmt.Sprintf("/api/runs/%d/transition", operatorRun.runID)
	if st, b := perform(t, engine, http.MethodPost, bumpPath, tokens["reviewer"], "rr-stale-hold",
		map[string]any{"status": "hold", "expectedVersion": operatorRun.runVersion, "reason": "fixture stale setup hold"}); st != http.StatusOK {
		t.Fatalf("stale setup hold status = %d body=%s", st, b)
	}
	if st, b := perform(t, engine, http.MethodPost, bumpPath, tokens["reviewer"], "rr-stale-proofing",
		map[string]any{"status": "proofing", "expectedVersion": operatorRun.runVersion + 1, "reason": "fixture stale setup proofing"}); st != http.StatusOK {
		t.Fatalf("stale setup proofing status = %d body=%s", st, b)
	}
	again := map[string]any{"expectedVersion": operatorRun.runVersion, "proofCodes": []string{operatorRun.proofCode}, "reason": "stale version release"}
	if status, body := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/runs/%d/release", operatorRun.runID), tokens["admin"], "run-release-stale", again); status != http.StatusConflict {
		t.Fatalf("stale version release status = %d, want 409 body=%s", status, body)
	}
	if after := countReleaseDecisions(t, engine, tokens["reviewer"]); after != before {
		t.Fatalf("release decisions changed on stale release: before=%d after=%d", before, after)
	}

	// The same run with the current version then releases successfully, proving
	// the conflict was purely the optimistic lock.
	okReq := map[string]any{"expectedVersion": operatorRun.runVersion + 2, "proofCodes": []string{operatorRun.proofCode}, "reason": "fresh version release"}
	if status, body := perform(t, engine, http.MethodPost, fmt.Sprintf("/api/runs/%d/release", operatorRun.runID), tokens["reviewer"], "run-release-fresh", okReq); status != http.StatusCreated {
		t.Fatalf("fresh version release status = %d, want 201 body=%s", status, body)
	}
}

type seededRun struct {
	ID      uint   `json:"id"`
	Status  string `json:"status"`
	Version uint   `json:"version"`
}

func findSeededRun(t *testing.T, engine *gin.Engine, token, code string) seededRun {
	t.Helper()
	status, body := perform(t, engine, http.MethodGet, "/api/runs?page=1&pageSize=100&search="+code, token, "run-seed-find-"+code, nil)
	if status != http.StatusOK {
		t.Fatalf("list runs status = %d body=%s", status, body)
	}
	page := decodeData[[]seededRun](t, body)
	for _, run := range page {
		// search also matches names; ensure exact code via re-read
		_, detail := perform(t, engine, http.MethodGet, fmt.Sprintf("/api/runs/%d", run.ID), token, "run-seed-read", nil)
		full := decodeData[struct {
			ID      uint   `json:"id"`
			Code    string `json:"code"`
			Status  string `json:"status"`
			Version uint   `json:"version"`
		}](t, detail)
		if full.Code == code {
			return seededRun{ID: full.ID, Status: full.Status, Version: full.Version}
		}
	}
	t.Fatalf("seeded run %s not found", code)
	return seededRun{}
}

func countReleaseDecisions(t *testing.T, engine *gin.Engine, token string) int64 {
	t.Helper()
	status, body := perform(t, engine, http.MethodGet, "/api/release?page=1&pageSize=1", token, "release-count", nil)
	if status != http.StatusOK {
		t.Fatalf("list release decisions status = %d body=%s", status, body)
	}
	var envelope struct {
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode release page: %v", err)
	}
	return envelope.Meta.Total
}

func decodeFailure(t *testing.T, body []byte) releaseFailure {
	t.Helper()
	var envelope struct {
		releaseFailure
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode failure envelope %s: %v", body, err)
	}
	return envelope.releaseFailure
}
