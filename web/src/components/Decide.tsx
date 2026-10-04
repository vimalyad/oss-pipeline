import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState, type FormEvent } from "react";
import { api } from "../api/client";
import type { CandidateStatus, Decision } from "../api/types";
import { REJECTABLE } from "../lib/status";
import { Icon } from "./Icon";

/**
 * Approve or reject one candidate.
 *
 * Neither action is optimistic. Approval is the gate that lets the engine
 * open a public pull request under your name, so the button reports what the
 * database actually accepted -- including "already approved" from a second
 * tap, and a refusal from the state machine -- rather than what was hoped.
 *
 * Reject asks for a reason in place, and will not submit without one: a
 * rejection with no reason looks like the scorer's, and only the scorer's
 * comes back for another look.
 */
export function Decide({
  slug,
  status,
  compact = false,
  onDecided,
}: {
  slug: string;
  status: CandidateStatus;
  compact?: boolean;
  /**
   * Called with what the server did. A list page passes this because the
   * decided item leaves the list on the refetch, taking its own result
   * message with it; the page reports the decision instead.
   */
  onDecided?: (d: Decision) => void;
}) {
  const qc = useQueryClient();
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const reasonId = useId();
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const settle = () => qc.invalidateQueries();
  const approve = useMutation({
    mutationFn: () => api.approve(slug),
    onSuccess: (d) => {
      onDecided?.(d);
      return settle();
    },
  });
  const reject = useMutation({
    mutationFn: () => api.reject(slug, reason.trim()),
    onSuccess: (d) => {
      setRejecting(false);
      setReason("");
      onDecided?.(d);
      return settle();
    },
  });

  const canApprove = status === "proposed";
  const canReject = REJECTABLE.includes(status);
  if (!canApprove && !canReject) return null;

  const busy = approve.isPending || reject.isPending;
  const done = approve.data ?? reject.data;
  const error = approve.error ?? reject.error;

  const submitReject = (e: FormEvent) => {
    e.preventDefault();
    if (!reason.trim()) {
      inputRef.current?.focus();
      return;
    }
    reject.mutate();
  };

  return (
    <div className={compact ? "decide decide-compact" : "decide"}>
      {!rejecting && (
        <div className="decide-actions">
          {canApprove && (
            <button type="button" className="btn btn-primary" disabled={busy} onClick={() => approve.mutate()}>
              <Icon name="check" size={14} />
              {approve.isPending ? "Approving…" : "Approve"}
            </button>
          )}
          {canReject && (
            <button
              type="button"
              className="btn btn-secondary"
              disabled={busy}
              onClick={() => {
                setRejecting(true);
                approve.reset();
              }}
            >
              <Icon name="x" size={14} />
              Reject…
            </button>
          )}
        </div>
      )}

      {rejecting && (
        <form className="decide-form" onSubmit={submitReject}>
          <label htmlFor={reasonId}>Why are you rejecting it?</label>
          <textarea
            id={reasonId}
            ref={inputRef}
            // The field mounts exactly when the form opens, so focusing on
            // mount moves the keyboard to it without a scheduled callback.
            autoFocus
            rows={2}
            value={reason}
            required
            placeholder="e.g. needs hardware this machine doesn't have"
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setRejecting(false);
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) submitReject(e);
            }}
          />
          <p className="field-hint">Kept in the audit log. A rejection you make is never reconsidered automatically.</p>
          <div className="decide-actions">
            <button type="submit" className="btn btn-danger" disabled={busy || !reason.trim()}>
              {reject.isPending ? "Rejecting…" : "Reject"}
            </button>
            <button type="button" className="btn btn-ghost" disabled={busy} onClick={() => setRejecting(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}

      <div aria-live="polite" className="decide-result">
        {error && (
          <p className="result result-error">
            <Icon name="alert" size={14} /> {error.message}
          </p>
        )}
        {done && !error && (
          <p className="result result-ok">
            <Icon name="check-circle" size={14} /> {done.message}
          </p>
        )}
      </div>
    </div>
  );
}
