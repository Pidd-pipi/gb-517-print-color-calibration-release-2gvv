import { useEffect, useMemo, useState } from 'react';
import { listColorProof } from '../api/color-proof';
import { releaseFromRun } from '../api/print-run';
import { ApiRequestError, type DomainRecord, type ProofIssue, type RunReleaseResult } from '../types/domain';
import { StatusBadge } from './common/StatusBadge';
import { UiButton } from './common/UiButton';

// RunReleasePanel is embedded in the print-run detail dialog. The reviewer
// picks the proof codes adopted by THIS run; the backend refuses to write the
// run or release decision when any proof is missing, not accepted or sourced
// from another run, and the offending proofs are highlighted inline.
export function RunReleasePanel({ run, onReleased }: { run: DomainRecord; onReleased: (result: RunReleaseResult) => void }) {
  const [proofs, setProofs] = useState<DomainRecord[]>([]);
  const [loadingProofs, setLoadingProofs] = useState(true);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [manualCode, setManualCode] = useState('');
  const [manualCodes, setManualCodes] = useState<string[]>([]);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [issues, setIssues] = useState<ProofIssue[]>([]);
  const [formError, setFormError] = useState('');
  const [success, setSuccess] = useState<RunReleaseResult | null>(null);

  const released = run.status === 'released';
  const relatedProofs = useMemo(
    () => proofs.filter((proof) => (proof.relatedCode || '').toUpperCase() === run.code.toUpperCase()).sort((a, b) => a.code.localeCompare(b.code)),
    [proofs, run.code],
  );
  const selectedCodes = useMemo(() => {
    const codes = new Set<string>();
    Object.entries(selected).forEach(([code, checked]) => { if (checked) codes.add(code.toUpperCase()); });
    manualCodes.forEach((code) => codes.add(code.toUpperCase()));
    return Array.from(codes);
  }, [selected, manualCodes]);
  const issueByCode = useMemo(() => {
    const map: Record<string, ProofIssue> = {};
    issues.forEach((issue) => { map[issue.code.toUpperCase()] = issue; });
    return map;
  }, [issues]);

  useEffect(() => {
    let cancelled = false;
    listColorProof(1, 100)
      .then((result) => { if (!cancelled) setProofs(result.data); })
      .catch(() => { if (!cancelled) setFormError('校样列表加载失败，请重试'); })
      .finally(() => { if (!cancelled) setLoadingProofs(false); });
    return () => { cancelled = true; };
  }, []);

  const addManualCode = () => {
    const code = manualCode.trim().toUpperCase();
    if (!code) return;
    setIssues([]);
    setManualCodes((prev) => (prev.includes(code) ? prev : [...prev, code]));
    setManualCode('');
  };
  const toggleProof = (code: string) => {
    setIssues([]);
    setSelected((prev) => ({ ...prev, [code]: !prev[code] }));
  };
  const removeManualCode = (code: string) => setManualCodes((prev) => prev.filter((item) => item !== code));

  const submit = async () => {
    if (!selectedCodes.length) { setFormError('请至少选择一份本批次采用的校样编号'); return; }
    if (reason.trim().length < 3) { setFormError('请填写不少于 3 个字的放行原因'); return; }
    setFormError('');
    setIssues([]);
    setSubmitting(true);
    try {
      const result = await releaseFromRun(run.id, { expectedVersion: run.version, proofCodes: selectedCodes, reason: reason.trim() });
      setSuccess(result.data);
      onReleased(result.data);
    } catch (error) {
      if (error instanceof ApiRequestError && error.details?.proofIssues?.length) {
        setIssues(error.details.proofIssues);
      } else {
        setFormError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setSubmitting(false);
    }
  };

  if (success) {
    return <section className="release-panel">
      <h3>按校样核对放行</h3>
      <div className="alert alert-success" role="status">
        放行成功：已生成放行记录 <strong>{success.releaseDecision.code}</strong>（关联批次 {success.run.code}），核对校样 {success.verifiedProofs.join('、')}。
      </div>
      <p className="release-hint">批次已迁移至 released，新的不可变修订与放行记录可分别在批次版本链和放行决定页查看。</p>
    </section>;
  }

  if (released) {
    return <section className="release-panel release-panel-done"><h3>批次已放行</h3><p>该批次已完成放行，如需复核请查看放行决定页的版本链。</p></section>;
  }

  if (run.status !== 'proofing') {
    return <section className="release-panel release-panel-done"><h3>按校样核对放行</h3><p>批次进入「校样中」状态后，才能在此核对校样并放行；当前状态为 {run.status}。</p></section>;
  }

  return <section className="release-panel">
    <h3>按校样核对放行</h3>
    <p className="release-hint">仅勾选本批次采用、且已接收的校样；任一份缺失、未接收或关联了其他批次时，批次和放行记录都不会写入。</p>
    {issues.length > 0 && <div className="alert" role="alert">
      校样核对未通过，批次未放行：
      <ul>{issues.map((issue) => <li key={issue.code}><strong>{issue.code}</strong>：{issue.reason}</li>)}</ul>
    </div>}
    {formError && <div className="alert" role="alert">{formError}</div>}
    {loadingProofs ? <div className="loading">正在加载校样…</div> : <div className="proof-check-list">
      {relatedProofs.length === 0 && <p className="release-empty">当前没有关联到本批次（{run.code}）的校样，可在下方手动输入校样编号核对。</p>}
      {relatedProofs.map((proof) => {
        const code = proof.code.toUpperCase();
        const issue = issueByCode[code];
        const accepted = proof.status === 'accepted';
        return <label key={proof.id} className={`proof-check-row${issue ? ' is-invalid' : ''}${selected[code] ? ' is-selected' : ''}`}>
          <input type="checkbox" checked={Boolean(selected[code])} onChange={() => toggleProof(code)} />
          <span className="proof-check-code">{proof.code}</span>
          <StatusBadge status={proof.status} />
          <span className="proof-check-meta">{proof.relatedCode} · {proof.metricValue} {proof.metricUnit}</span>
          {!accepted && <small className="proof-check-warn">尚未接收</small>}
          {issue && <small className="proof-check-error">{issue.reason}</small>}
        </label>;
      })}
      {manualCodes.map((code) => {
        const known = proofs.find((proof) => proof.code.toUpperCase() === code);
        const issue = issueByCode[code];
        return <label key={code} className={`proof-check-row is-manual${issue ? ' is-invalid' : ''} is-selected`}>
          <input type="checkbox" checked onChange={() => removeManualCode(code)} aria-label={`移除 ${code}`} />
          <span className="proof-check-code">{code}</span>
          {known ? <StatusBadge status={known.status} /> : <small className="proof-check-warn">系统中未找到</small>}
          <span className="proof-check-meta">{known?.relatedCode || '来源未知'}</span>
          {issue && <small className="proof-check-error">{issue.reason}</small>}
        </label>;
      })}
    </div>}
    <div className="proof-manual-row">
      <input aria-label="手动输入校样编号" placeholder="手动输入其他校样编号，如 CP-009" value={manualCode}
        onChange={(event) => setManualCode(event.target.value)}
        onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); addManualCode(); } }} />
      <button type="button" className="link-button" onClick={addManualCode}>添加校样</button>
    </div>
    <textarea aria-label="放行原因" placeholder="放行原因（至少 3 个字），将写入批次修订和放行记录" value={reason}
      onChange={(event) => setReason(event.target.value)} maxLength={500} />
    <div className="release-actions">
      <span className="release-selected">已选 {selectedCodes.length} 份：{selectedCodes.join('、') || '无'}</span>
      <UiButton onClick={() => void submit()} disabled={submitting}>{submitting ? '核对并放行中…' : '核对校样并放行批次'}</UiButton>
    </div>
  </section>;
}
