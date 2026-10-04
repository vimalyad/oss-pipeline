import { absolute, relative } from "../lib/time";

/** Relative on screen, exact on hover and to assistive tech. */
export function Time({ iso, className }: { iso: string | null; className?: string }) {
  if (!iso) return <span className={className}>—</span>;
  return (
    <time dateTime={iso} title={absolute(iso)} className={className}>
      {relative(iso)}
    </time>
  );
}
