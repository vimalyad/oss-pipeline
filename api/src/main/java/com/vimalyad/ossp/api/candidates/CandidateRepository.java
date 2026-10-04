package com.vimalyad.ossp.api.candidates;

import static com.vimalyad.ossp.api.Rows.integer;
import static com.vimalyad.ossp.api.Rows.strings;
import static com.vimalyad.ossp.api.Rows.ts;

import com.vimalyad.ossp.api.candidates.Models.AuditEntry;
import com.vimalyad.ossp.api.candidates.Models.Brief;
import com.vimalyad.ossp.api.candidates.Models.CandidateSummary;
import com.vimalyad.ossp.api.candidates.Models.HistoryEntry;
import com.vimalyad.ossp.api.candidates.Models.Signal;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.util.List;
import java.util.Optional;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;

/**
 * Reads candidates and everything recorded about them.
 *
 * <p>Plain SQL over JdbcClient rather than JPA entities: the tables carry
 * Postgres enums and arrays, the views are read models with no identity of
 * their own, and the schema belongs to db/migrations. An ORM here would be a
 * second description of a schema this service does not own.
 */
@Repository
public class CandidateRepository {
    private static final String SUMMARY = """
            SELECT c.id, c.slug, r.full_name AS repo, c.issue_number, c.title, c.url,
                   c.status::text AS status, c.labels, c.contest::text AS contest,
                   c.reject_kind::text AS reject_kind, c.reject_reason,
                   c.blockers, c.soft_penalties, c.score_failures,
                   c.issue_created_at, c.updated_at,
                   (SELECT p.id FROM pull_requests p WHERE p.candidate_id = c.id
                     ORDER BY p.opened_at DESC LIMIT 1) AS pr_id
              FROM candidates c
              JOIN repos r ON r.id = c.repo_id
            """;

    private final JdbcClient db;

    public CandidateRepository(JdbcClient db) {
        this.db = db;
    }

    public List<CandidateSummary> byStatus(List<String> statuses, int limit) {
        return db.sql(SUMMARY + " WHERE c.status::text IN (:statuses) ORDER BY c.updated_at DESC LIMIT :limit")
                .param("statuses", statuses)
                .param("limit", limit)
                .query(CandidateRepository::summary)
                .list();
    }

    public Optional<CandidateSummary> bySlug(String slug) {
        return db.sql(SUMMARY + " WHERE c.slug = :slug")
                .param("slug", slug)
                .query(CandidateRepository::summary)
                .optional();
    }

    public List<HistoryEntry> history(long candidateId) {
        return db.sql("""
                SELECT from_status::text AS from_status, to_status::text AS to_status,
                       note, forced, actor, at
                  FROM candidate_history WHERE candidate_id = :id ORDER BY at, id
                """)
                .param("id", candidateId)
                .query((rs, n) -> new HistoryEntry(
                        rs.getString("from_status"), rs.getString("to_status"), rs.getString("note"),
                        rs.getBoolean("forced"), rs.getString("actor"), ts(rs, "at")))
                .list();
    }

    public Optional<Brief> brief(long candidateId) {
        return db.sql("SELECT * FROM briefs WHERE candidate_id = :id")
                .param("id", candidateId)
                .query((rs, n) -> new Brief(
                        rs.getString("maintainer_approach"), rs.getString("approach_source_url"),
                        rs.getString("approach_association"), strings(rs, "rejected_approaches"),
                        strings(rs, "acceptance_criteria"), strings(rs, "open_questions"),
                        rs.getString("claimed_by"), rs.getString("reproduction"),
                        strings(rs, "dropped"), ts(rs, "extracted_at")))
                .optional();
    }

    public Optional<Signal> signal(long candidateId) {
        return db.sql("SELECT * FROM pr_signals WHERE candidate_id = :id")
                .param("id", candidateId)
                .query((rs, n) -> new Signal(
                        rs.getInt("pr_number"), rs.getString("url"), rs.getString("author"),
                        rs.getBoolean("is_draft"), integer(rs, "days_since_commit"),
                        integer(rs, "days_since_author_comment"), rs.getBoolean("reviewed"),
                        rs.getBoolean("checks_failing"), strings(rs, "reasons")))
                .optional();
    }

    public List<AuditEntry> audit(String slug, int limit) {
        String where = slug == null ? "" : " WHERE slug = :slug";
        var spec = db.sql("SELECT id, action, slug, detail, actor, at FROM audit" + where
                + " ORDER BY at DESC, id DESC LIMIT :limit").param("limit", limit);
        if (slug != null) {
            spec = spec.param("slug", slug);
        }
        return spec.query((rs, n) -> new AuditEntry(rs.getLong("id"), rs.getString("action"),
                        rs.getString("slug"), rs.getString("detail"), rs.getString("actor"), ts(rs, "at")))
                .list();
    }

    static CandidateSummary summary(ResultSet rs, int n) throws SQLException {
        long pr = rs.getLong("pr_id");
        return new CandidateSummary(
                rs.getLong("id"), rs.getString("slug"), rs.getString("repo"), rs.getInt("issue_number"),
                rs.getString("title"), rs.getString("url"), rs.getString("status"), strings(rs, "labels"),
                rs.getString("contest"), rs.getString("reject_kind"), rs.getString("reject_reason"),
                strings(rs, "blockers"), strings(rs, "soft_penalties"), strings(rs, "score_failures"),
                ts(rs, "issue_created_at"), ts(rs, "updated_at"), rs.wasNull() ? null : pr);
    }
}
