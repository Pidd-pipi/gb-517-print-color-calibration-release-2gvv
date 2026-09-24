import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { checkRunRelease, releaseRunWithProofs } from '../api/batch-release';
import type { BatchReleaseCheck, DomainRecord, ProofCheck } from '../types/domain';
import type { RunState } from '../types/status';
import { formatDate } from '../utils/format';
import { StatusBadge } from './common/StatusBadge';
import { RunStateBadge } from './common/RunStateBadge';
import { UiButton } from './common/UiButton';

const PROOF_ISSUE_LABEL: Record<string, string> = {
  missing: '校样遗漏',
  not_accepted: '未接收',
  wrong_run: '来源不一致',
  duplicated: '编号重复',
};

// RunReleaseDialog is the reviewer's checked-release flow launched from a run
// detail view: pick the exact proof codes adopted by this batch, confirm each
// one is accepted and linked to this run, then create the traceable release
// record. The standalone /release entry is untouched.
export function RunReleaseDialog({ run, onClose, onReleased }: {
  run: DomainRecord;
  onClose: () => void;
  onReleased: () => void;
}) {
  const [check, setCheck] = useState<BatchReleaseCheck | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [newCode, setNewCode] = useState('');
  const [reason, setReason] = useState('');
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const initializedRef = useRef(false);

  const selectedCodes = useMemo(() => Array.from(selected), [selected]);

  const refresh = useCallback(async (codes: string[]) => {
    setLoading(true);
    setError('');
    try {
      const result = await checkRunRelease(run.id, codes.length ? codes : undefined);
      setCheck(result.data);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [run.id]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    checkRunRelease(run.id)
      .then((result) => {
        if (cancelled) return;
        setCheck(result.data);
        setSelected(new Set(result.data.proofs.map((proof) => proof.code)));
        initializedRef.current = true;
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [run.id]);

  useEffect(() => {
    if (!initializedRef.current) return;
    void refresh(selectedCodes);
    // Only re-verify when the selection changes; refresh itself is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected]);

  const addCode = () => {
    const code = newCode.trim().toUpperCase();
    if (!code || selected.has(code)) { setNewCode(''); return; }
    setSelected((prev) => new Set(prev).add(code));
    setNewCode('');
  };

  const toggle = (code: string) => setSelected((prev) => {
    const next = new Set(prev);
    if (next.has(code)) next.delete(code); else next.add(code);
    return next;
  });

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      const result = await releaseRunWithProofs(run.id, selectedCodes, run.version, reason.trim());
      setSuccess(`已生成放行记录 ${result.data.decision.code}，批次 ${run.code} 已放行`);
      setTimeout(() => { onReleased(); onClose(); }, 900);
    } catch (err) {
      const failure = err as Error & { status?: number; payload?: { data?: { check?: BatchReleaseCheck } } };
      if (failure.status === 422 && failure.payload?.data?.check) {
        setCheck(failure.payload.data.check);
        const failed = failure.payload.data.check.proofs.filter((proof) => !proof.accepted);
        setError(failed.map((proof) => `${proof.code}：${proof.message || PROOF_ISSUE_LABEL[proof.issue || ''] || '核对未通过'}`).join('；'));
      } else {
        setError(failure.message || String(err));
      }
    } finally {
      setSubmitting(false);
    }
  };

  const failed = check?.proofs.filter((proof) => !proof.accepted) ?? [];
  const ready = Boolean(check?.ready && reason.trim().length >= 3 && selectedCodes.length > 0 && !submitting);

  const renderProof = (proof: ProofCheck) =>
    <li key={proof.code} className={proof.accepted ? 'proof-check-row' : 'proof-check-row proof-check-row--failed'}>
      <label>
        <input type="checkbox" checked={selected.has(proof.code)} onChange={() => toggle(proof.code)} />
        <strong>{proof.code}</strong>
        {proof.name && <small>{proof.name}</small>}
      </label>
      <div className="proof-check-meta">
        {proof.status ? <StatusBadge status={proof.status} /> : <span className="status status--danger">未找到</span>}
        {proof.runCode !== undefined && <span>关联批次：{proof.runCode || '未关联'}</span>}
        {!proof.accepted && <em className="proof-check-issue">[{PROOF_ISSUE_LABEL[proof.issue || ''] || '核对未通过'}] {proof.message}</em>}
      </div>
    </li>;

  return <div className="modal-backdrop"><section className="modal modal--wide" role="dialog" aria-modal="true">
    <h2>批次放行 · 校样逐条核对</h2>
    <div className="release-run-head">
      <div><strong>{run.code}</strong><small>{run.name}</small></div>
      <RunStateBadge state={run.status as RunState} />
    </div>
    {check && !check.transitionValid && <div className="alert" role="alert">{check.transitionIssue || '当前批次状态不允许放行'}</div>}
    {error && <div className="alert" role="alert">{error}</div>}
    {success && <div className="alert alert--success" role="status">{success}</div>}

    <p className="release-hint">勾选本批次采用的校样编号；每份校样都必须已被复核员接收，且关联批次为 <strong>{run.code}</strong>。任一份不满足，批次与放行记录都不会写入。</p>
    <div className="proof-add-row">
      <input aria-label="添加校样编号" placeholder="手动输入其他校样编号，如 CP-007" value={newCode}
        onChange={(event) => setNewCode(event.target.value)}
        onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); addCode(); } }} />
      <UiButton onClick={addCode}>添加编号</UiButton>
    </div>
    <ul className="proof-check-list" aria-busy={loading}>
      {check?.proofs.map(renderProof)}
      {!loading && (!check || check.proofs.length === 0) && <li className="proof-check-empty">该批次目前没有关联校样，请手动添加编号核对。</li>}
    </ul>
    {failed.length > 0 && <p className="proof-check-summary">以下校样存在问题，暂不能放行：{failed.map((proof) => proof.code).join('、')}</p>}
    <label className="release-reason">
      <span>放行说明（必填）</span>
      <textarea rows={2} maxLength={500} value={reason} onChange={(event) => setReason(event.target.value)} placeholder="例如：全部色组 ΔE 达标，客户签样一致" />
    </label>
    <footer>
      <button className="link-button" onClick={onClose} disabled={submitting}>取消</button>
      <UiButton onClick={() => void submit()} disabled={!ready}>核对无误并放行</UiButton>
    </footer>
    <small className="release-version-note">当前批次版本 v{run.version}，放行成功后批次与放行记录都会留下不可变修订（{formatDate(run.updatedAt)} 数据）。</small>
  </section></div>;
}
