package com.vimalyad.ossp.api.board;

import static com.vimalyad.ossp.api.Rows.integer;
import static com.vimalyad.ossp.api.Rows.ts;

import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.Optional;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;

/**
 * Pull requests, read through v_pr_board so this service and the engine agree
 * on what a card shows. The join back to candidates only adds the slug, which
 * the view leaves out and the frontend needs to link a card to its candidate.
 */
@Repository
public class PrRepository {
    public record PrCard(
            long id, int number, String repo, String language, Integer stars,
            String slug, int issueNumber, String title, String issueUrl, String prUrl,
            String state, int checksTotal, int checksFailing, int checksPending,
            String reviewDecision, int reviewerCount, int unansweredItems,
            Integer additions, Integer deletions, Integer changedFiles,
            OffsetDateTime openedAt, OffsetDateTime remoteUpdatedAt,
            OffsetDateTime mergedAt, OffsetDateTime closedAt,
            Integer idleDays, boolean tookOver, String credits, int openFeedback) {}

    public record StateChange(String from, String to, String detail, OffsetDateTime at) {}

    public record Feedback(
            long id, String author, String authorAssociation, String kind, String body, String url,
            String classification, OffsetDateTime createdAt, String draft, boolean posted,
            String postedUrl, OffsetDateTime postedAt) {}

    private static final String CARD = """
            SELECT b.*, b.state::text AS state_text, c.slug
              FROM v_pr_board b
              JOIN pull_requests p ON p.id = b.id
              JOIN candidates c ON c.id = p.candidate_id
            """;

    private final JdbcClient db;

    public PrRepository(JdbcClient db) {
        this.db = db;
    }

    /** {@code live} excludes merged and closed; the board's default view. */
    public List<PrCard> list(boolean live) {
        String where = live ? " WHERE b.state NOT IN ('merged','closed')" : "";
        return db.sql(CARD + where + " ORDER BY COALESCE(b.remote_updated_at, b.opened_at) DESC")
                .query(PrRepository::card)
                .list();
    }

    public Optional<PrCard> byId(long id) {
        return db.sql(CARD + " WHERE b.id = :id").param("id", id).query(PrRepository::card).optional();
    }

    public long candidateId(long prId) {
        return db.sql("SELECT candidate_id FROM pull_requests WHERE id = :id")
                .param("id", prId).query(Long.class).single();
    }

    public List<StateChange> states(long prId) {
        return db.sql("""
                SELECT from_state::text AS from_state, to_state::text AS to_state, detail, at
                  FROM pr_state_history WHERE pr_id = :id ORDER BY at, id
                """)
                .param("id", prId)
                .query((rs, n) -> new StateChange(rs.getString("from_state"), rs.getString("to_state"),
                        rs.getString("detail"), ts(rs, "at")))
                .list();
    }

    public List<Feedback> feedback(long prId) {
        return db.sql("SELECT * FROM feedback_items WHERE pr_id = :id ORDER BY created_at, id")
                .param("id", prId)
                .query((rs, n) -> new Feedback(
                        rs.getLong("id"), rs.getString("author"), rs.getString("author_association"),
                        rs.getString("kind"), rs.getString("body"), rs.getString("url"),
                        rs.getString("classification"), ts(rs, "created_at"), rs.getString("draft"),
                        rs.getBoolean("posted"), rs.getString("posted_url"), ts(rs, "posted_at")))
                .list();
    }

    static PrCard card(ResultSet rs, int n) throws SQLException {
        return new PrCard(
                rs.getLong("id"), rs.getInt("number"), rs.getString("repo"), rs.getString("language"),
                integer(rs, "stars"), rs.getString("slug"), rs.getInt("issue_number"), rs.getString("title"),
                rs.getString("issue_url"), rs.getString("pr_url"), rs.getString("state_text"),
                rs.getInt("checks_total"), rs.getInt("checks_failing"), rs.getInt("checks_pending"),
                rs.getString("review_decision"), rs.getInt("reviewer_count"), rs.getInt("unanswered_items"),
                integer(rs, "additions"), integer(rs, "deletions"), integer(rs, "changed_files"),
                ts(rs, "opened_at"), ts(rs, "remote_updated_at"), ts(rs, "merged_at"), ts(rs, "closed_at"),
                integer(rs, "idle_days"), rs.getBoolean("took_over"), rs.getString("credits"),
                rs.getInt("open_feedback"));
    }
}
