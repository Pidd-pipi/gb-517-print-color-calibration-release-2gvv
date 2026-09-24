package router_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/config"
	"github.com/blueship581/print-color-calibration-release/backend/internal/database"
	"github.com/blueship581/print-color-calibration-release/backend/internal/router"
	"github.com/gin-gonic/gin"
)

// setupTestDSN returns an isolated SQLite DSN for a test.
func setupTestDSN(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name+".db")
}

func setupTestRouter(t *testing.T, cfg config.Config) (*gin.Engine, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, redisClient, err := database.Open(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	engine := router.New(cfg, db, redisClient, logger)
	cancel := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	return engine, cancel
}

// runProofFixture holds a run advanced to proofing together with an accepted
// proof sourced from that run.
type runProofFixture struct {
	runID      uint
	runVersion uint
	proofCode  string
}

// createRunAndProof builds a run: setup -> printing -> proofing and one
// accepted proof whose relatedCode matches the run.
func createRunAndProof(t *testing.T, engine *gin.Engine, tokens map[string]string) runProofFixture {
	t.Helper()
	suffix := time.Now().UTC().Format("150405.000000")
	runCode := "PR-RR-" + suffix
	proofCode := "CP-RR-" + suffix
	now := time.Now().UTC().Format(time.RFC3339)

	runPayload := map[string]any{
		"code": runCode, "name": "放行链路测试批次", "description": "run release router test",
		"facility": "测试印刷区", "owner": "operator", "category": "校准",
		"riskLevel": "medium", "metricValue": 1.8, "metricUnit": "dE",
		"effectiveAt": now, "evidence": "plated and ink balanced", "relatedCode": runCode,
	}
	status, body := perform(t, engine, http.MethodPost, "/api/runs", tokens["operator"], "rr-run-create", runPayload)
	if status != http.StatusCreated {
		t.Fatalf("create run status = %d body=%s", status, body)
	}
	run := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, body)

	advance := func(target, requestID string, version uint) {
		req := map[string]any{"status": target, "expectedVersion": version, "reason": "fixture advance " + target}
		st, b := perform(t, engine, http.MethodPost, "/api/runs/"+uintString(run.ID)+"/transition", tokens["operator"], requestID, req)
		if st != http.StatusOK {
			t.Fatalf("advance run to %s status = %d body=%s", target, st, b)
		}
	}
	advance("printing", "rr-run-printing", run.Version)
	advance("proofing", "rr-run-proofing", run.Version+1)

	proofPayload := map[string]any{
		"code": proofCode, "name": "放行链路测试校样", "description": "run release router test proof",
		"facility": "测试印刷区", "owner": "reviewer", "category": "校准",
		"riskLevel": "low", "metricValue": 1.1, "metricUnit": "dE",
		"effectiveAt": now, "evidence": "spectrophotometer accepted reading", "relatedCode": runCode,
	}
	st, b := perform(t, engine, http.MethodPost, "/api/proofs", tokens["operator"], "rr-proof-create", proofPayload)
	if st != http.StatusCreated {
		t.Fatalf("create proof status = %d body=%s", st, b)
	}
	proof := decodeData[struct {
		ID      uint `json:"id"`
		Version uint `json:"version"`
	}](t, b)
	reviewReq := map[string]any{"status": "review", "expectedVersion": proof.Version, "reason": "fixture submit for review"}
	if st, b = perform(t, engine, http.MethodPost, "/api/proofs/"+uintString(proof.ID)+"/transition", tokens["operator"], "rr-proof-review", reviewReq); st != http.StatusOK {
		t.Fatalf("proof to review status = %d body=%s", st, b)
	}
	acceptReq := map[string]any{"status": "accepted", "expectedVersion": proof.Version + 1, "reason": "fixture accept proof"}
	if st, b = perform(t, engine, http.MethodPost, "/api/proofs/"+uintString(proof.ID)+"/transition", tokens["reviewer"], "rr-proof-accept", acceptReq); st != http.StatusOK {
		t.Fatalf("proof accept status = %d body=%s", st, b)
	}

	return runProofFixture{runID: run.ID, runVersion: run.Version + 2, proofCode: proofCode}
}
