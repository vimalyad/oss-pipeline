import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { PageHead } from "../components/Layout";
import { Empty, Failed, Skeleton } from "../components/States";
import { AuditTable } from "./CandidatePage";

export function AuditPage() {
  const audit = useQuery({ queryKey: ["audit"], queryFn: () => api.audit(500) });
  return (
    <>
      <PageHead
        title="Audit log"
        sub="Everything that changed the outside world or a decision about a candidate. Append-only: the database refuses edits and deletes."
      />
      {audit.isPending ? (
        <Skeleton rows={10} height={36} />
      ) : audit.isError ? (
        <Failed error={audit.error} retry={() => audit.refetch()} />
      ) : audit.data.length === 0 ? (
        <Empty icon="log" title="Nothing recorded yet" />
      ) : (
        <AuditTable rows={audit.data} />
      )}
    </>
  );
}
