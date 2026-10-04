package com.vimalyad.ossp.api.candidates;

import java.time.OffsetDateTime;
import java.util.List;

/** The shapes the candidate endpoints return. One file, because they are only data. */
public final class Models {
    private Models() {}

    public record CandidateSummary(
            long id,
            String slug,
            String repo,
            int issueNumber,
            String title,
            String url,
            String status,
            List<String> labels,
            String contest,
            String rejectKind,
            String rejectReason,
            List<String> blockers,
            List<String> softPenalties,
            List<String> scoreFailures,
            OffsetDateTime issueCreatedAt,
            OffsetDateTime updatedAt,
            Long prId) {}

    /** One edge of the lifecycle. {@code forced} marks a sanctioned reopen, with who did it. */
    public record HistoryEntry(
            String from, String to, String note, boolean forced, String actor, OffsetDateTime at) {}

    public record Brief(
            String maintainerApproach,
            String approachSourceUrl,
            String approachAssociation,
            List<String> rejectedApproaches,
            List<String> acceptanceCriteria,
            List<String> openQuestions,
            String claimedBy,
            String reproduction,
            List<String> dropped,
            OffsetDateTime extractedAt) {}

    /** Contest evidence: the existing pull request that most justified the verdict. */
    public record Signal(
            int prNumber,
            String url,
            String author,
            boolean draft,
            Integer daysSinceCommit,
            Integer daysSinceAuthorComment,
            boolean reviewed,
            boolean checksFailing,
            List<String> reasons) {}

    public record AuditEntry(long id, String action, String slug, String detail, String actor, OffsetDateTime at) {}

    public record CandidateDetail(
            CandidateSummary candidate,
            Brief brief,
            Signal signal,
            List<HistoryEntry> history,
            List<AuditEntry> audit) {}

    /** What a decision did. {@code changed} is false for a second tap on the same button. */
    public record Decision(String slug, String status, boolean changed, String message) {}
}
