package repository

import (
	"context"

	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BatchReleaseResult groups the aggregates persisted by one checked batch
// release so callers can audit each of them after the transaction commits.
type BatchReleaseResult struct {
	Run              model.PrintRun
	RunBefore        string
	Decision         model.ReleaseDecision
	Proofs           []model.ColorProof
	RunRevision      model.PrintRunRevision
	DecisionRevision model.ReleaseDecisionRevision
}

// BatchReleaseRepository owns the single cross-aggregate transaction that
// releases a 印刷批次 together with its traceable 放行决定. Validation lives in
// the service; nothing here is written unless every proof check passes.
type BatchReleaseRepository interface {
	ReleaseRun(ctx context.Context, run *model.PrintRun, expectedVersion uint,
		decision *model.ReleaseDecision, acceptedProofs []model.ColorProof,
		actor, requestID, runReason, decisionReason string) (BatchReleaseResult, error)
}

type batchReleaseRepository struct{ db *gorm.DB }

func NewBatchReleaseRepository(db *gorm.DB) BatchReleaseRepository {
	return &batchReleaseRepository{db: db}
}

func (r *batchReleaseRepository) ReleaseRun(ctx context.Context, run *model.PrintRun, expectedVersion uint,
	decision *model.ReleaseDecision, acceptedProofs []model.ColorProof,
	actor, requestID, runReason, decisionReason string) (BatchReleaseResult, error) {
	result := BatchReleaseResult{RunBefore: "", Proofs: make([]model.ColorProof, 0, len(acceptedProofs))}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.PrintRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", run.ID).First(&before).Error; err != nil {
			return err
		}
		result.RunBefore = before.Status
		update := tx.Model(&model.PrintRun{}).
			Where("id = ? AND version = ?", run.ID, expectedVersion).
			Select("*").Omit("id", "code", "created_at", "deleted_at", "Revisions").Updates(run)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return ErrVersionConflict
		}
		runRevision := printRunRevision(run, actor, requestID, runReason)
		if err := tx.Create(runRevision).Error; err != nil {
			return err
		}

		if err := tx.Omit("Revisions").Create(decision).Error; err != nil {
			return err
		}
		decisionRevision := releaseDecisionRevision(decision, actor, requestID, decisionReason)
		if err := tx.Create(decisionRevision).Error; err != nil {
			return err
		}

		// Re-check the proofs inside the transaction: a proof that was
		// rejected or re-linked after preflight must abort the whole release.
		for _, expected := range acceptedProofs {
			var current model.ColorProof
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", expected.ID).First(&current).Error; err != nil {
				return err
			}
			if current.Status != model.ColorProofAcceptedStatus || current.RelatedCode != run.Code {
				return ErrVersionConflict
			}
			result.Proofs = append(result.Proofs, current)
		}

		if err := tx.Where("id = ?", run.ID).Preload("Revisions", func(db *gorm.DB) *gorm.DB {
			return db.Order("version DESC")
		}).First(&result.Run).Error; err != nil {
			return err
		}
		result.Decision = *decision
		result.RunRevision = *runRevision
		result.DecisionRevision = *decisionRevision
		return nil
	})
	return result, err
}
