
import { request } from './client';
import type { BatchReleaseCheck, BatchReleaseOutcome } from '../types/domain';

// Preflight the proof checklist for the reviewed release flow started on a run
// detail view. Without explicit codes the backend returns every proof linked
// to the run.
export async function checkRunRelease(runId: number, proofCodes?: string[]) {
  const query = proofCodes && proofCodes.length
    ? `?proofCodes=${proofCodes.map(encodeURIComponent).join('&proofCodes=')}`
    : '';
  return request<BatchReleaseCheck>(`/runs/${runId}/release-check${query}`);
}

export async function releaseRunWithProofs(
  runId: number,
  proofCodes: string[],
  expectedVersion: number,
  reason: string,
) {
  return request<BatchReleaseOutcome>(`/runs/${runId}/release`, {
    method: 'POST',
    body: JSON.stringify({ proofCodes, expectedVersion, reason }),
  });
}
