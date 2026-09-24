package dto

// BatchReleaseRequest is the contract for a reviewer-driven release started
// from a 印刷批次 detail view. The reviewer selects the exact proof codes this
// run was calibrated against; the service rejects the release if any of them is
// missing, not yet accepted, or linked to another batch.
type BatchReleaseRequest struct {
	ProofCodes      []string `json:"proofCodes" binding:"required,min=1,dive,max=64"`
	ExpectedVersion uint     `json:"expectedVersion" binding:"required"`
	Reason          string   `json:"reason" binding:"required,min=3,max=500"`
}

// BatchReleasePreflightQuery allows the UI to prefill the proof checklist.
// Without proofCodes every proof currently linked to the run is returned.
type BatchReleasePreflightQuery struct {
	ProofCodes []string `form:"proofCodes"`
}
