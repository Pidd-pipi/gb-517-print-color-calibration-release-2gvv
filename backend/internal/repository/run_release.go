package repository

import (
	"context"
	"time"

	"github.com/blueship581/print-color-calibration-release/backend/internal/model"
	"gorm.io/gorm"
)

// RunReleaseRepository performs the cross-aggregate writes of a batch release
// in a single transaction: the run moves to released with an immutable colour
// revision, a release decision that traces the run is appended with its own
// revision, and both state changes are audited. Any failure rolls every write
// back so a run can never be released without its decision record.
type RunReleaseRepository interface {
	ReleaseRun(ctx context.Context, input RunReleaseInput) (model.PrintRun, model.ReleaseDecision, error)
}

// RunReleaseInput carries the already validated aggregate states into the
// atomic release transaction.
type RunReleaseInput struct {
	Run                 *model.PrintRun
	PreviousStatus      string
	ExpectedVersion     uint
	Decision            *model.ReleaseDecision
	Actor               string
	RequestID           string
	RunReason           string
	DecisionReason      string
	RunAuditDetail      string
	DecisionAuditDetail string
}

type runReleaseRepository struct{ db *gorm.DB }

func NewRunReleaseRepository(db *gorm.DB) RunReleaseRepository {
	return &runReleaseRepository{db: db}
}

func (r *runReleaseRepository) ReleaseRun(ctx context.Context, input RunReleaseInput) (model.PrintRun, model.ReleaseDecision, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		run := input.Run
		runResult := tx.Model(&model.PrintRun{}).Where("id = ? AND version = ?", run.ID, input.ExpectedVersion).
			Select("*").Omit("id", "code", "created_at", "deleted_at", "Revisions").Updates(run)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected == 0 {
			return ErrVersionConflict
		}
		if err := tx.Create(printRunRevision(run, input.Actor, input.RequestID, input.RunReason)).Error; err != nil {
			return err
		}

		decision := input.Decision
		if err := tx.Omit("Revisions").Create(decision).Error; err != nil {
			return err
		}
		if err := tx.Create(releaseDecisionRevision(decision, input.Actor, input.RequestID, input.DecisionReason)).Error; err != nil {
			return err
		}

		auditRows := []model.AuditLog{
			{RequestID: input.RequestID, Actor: input.Actor, Action: "transition", EntityType: "PrintRun",
				EntityID: run.ID, BeforeState: input.PreviousStatus, AfterState: run.Status,
				Detail: input.RunAuditDetail, CreatedAt: now},
			{RequestID: input.RequestID, Actor: input.Actor, Action: "release", EntityType: "ReleaseDecision",
				EntityID: decision.ID, BeforeState: "", AfterState: decision.Status,
				Detail: input.DecisionAuditDetail, CreatedAt: now},
		}
		return tx.Create(&auditRows).Error
	})
	if err != nil {
		return model.PrintRun{}, model.ReleaseDecision{}, err
	}
	return *input.Run, *input.Decision, nil
}
