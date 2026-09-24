
import { request } from './client';
import type { DomainRecord, RunReleaseInput, RunReleaseResult } from '../types/domain';

export async function listPrintRun(page = 1, pageSize = 20, search = '') {
  return request<DomainRecord[]>(`/runs?page=${page}&pageSize=${pageSize}&search=${encodeURIComponent(search)}`);
}
export async function createPrintRun(input: Partial<DomainRecord>) {
  return request<DomainRecord>('/runs', { method: 'POST', body: JSON.stringify(input) });
}
export async function transitionPrintRun(id: number, status: string, expectedVersion: number, reason: string) {
  return request<DomainRecord>(`/runs/${id}/transition`, {
    method: 'POST', body: JSON.stringify({ status, expectedVersion, reason }),
  });
}
// releaseFromRun drives the batch-detail release workflow: every adopted proof
// must exist, be accepted and reference this run, otherwise the backend writes
// neither the run nor the release decision.
export async function releaseFromRun(id: number, input: RunReleaseInput) {
  return request<RunReleaseResult>(`/runs/${id}/release`, {
    method: 'POST', body: JSON.stringify(input),
  });
}
