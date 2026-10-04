import type { ReactNode } from "react";
import { ApiError } from "../api/client";
import { Icon, type IconName } from "./Icon";

export function Empty({ icon = "check-circle", title, children }: { icon?: IconName; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <Icon name={icon} size={20} />
      <p className="empty-title">{title}</p>
      {children && <p className="empty-body">{children}</p>}
    </div>
  );
}

export function Failed({ error, retry }: { error: unknown; retry?: () => void }) {
  const offline = error instanceof TypeError;
  const message = error instanceof ApiError || error instanceof Error ? error.message : String(error);
  return (
    <div className="failed" role="alert">
      <Icon name="alert" size={18} />
      <div>
        <p className="failed-title">{offline ? "Can't reach the API" : "That didn't load"}</p>
        <p className="failed-body">{offline ? "Is the api service running on port 8080?" : message}</p>
      </div>
      {retry && (
        <button type="button" className="btn btn-ghost" onClick={retry}>
          <Icon name="refresh" size={14} /> Retry
        </button>
      )}
    </div>
  );
}

/** Placeholder rows shaped like the content, so the layout does not jump when data lands. */
export function Skeleton({ rows = 4, height = 44 }: { rows?: number; height?: number }) {
  return (
    <div className="skeleton" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skeleton-row" style={{ height }} />
      ))}
    </div>
  );
}
