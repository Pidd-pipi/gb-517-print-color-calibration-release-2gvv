package dto

// RunReleaseRequest is the batch-release contract initiated from a print run
// detail. The reviewer chooses the proof codes adopted by the run; the service
// only releases when every proof exists, is accepted and is sourced from the
// same run.
type RunReleaseRequest struct {
	ExpectedVersion uint     `json:"expectedVersion" binding:"required"`
	ProofCodes      []string `json:"proofCodes" binding:"required,min=1,dive,min=2,max=64"`
	Reason          string   `json:"reason" binding:"required,min=3,max=500"`
	DecisionCode    string   `json:"decisionCode" binding:"omitempty,min=2,max=64"`
}

// ProofReleaseIssue pinpoints the single proof that blocks a batch release so
// the batch detail can highlight it directly.
type ProofReleaseIssue struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// RunReleaseResult returns the released run together with the release decision
// that traces back to the run via RelatedCode.
type RunReleaseResult struct {
	Run             any                 `json:"run"`
	ReleaseDecision any                 `json:"releaseDecision"`
	VerifiedProofs  []string            `json:"verifiedProofs"`
	ProofIssues     []ProofReleaseIssue `json:"proofIssues,omitempty"`
}
