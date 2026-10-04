package com.vimalyad.ossp.api.candidates;

import com.vimalyad.ossp.api.Operator;
import com.vimalyad.ossp.api.candidates.Models.Decision;
import org.springframework.dao.DataAccessException;
import org.springframework.http.HttpStatus;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;
import org.springframework.web.server.ResponseStatusException;

/**
 * The human gate, as the dashboard drives it. Mirrors engine/internal/gate so a
 * decision means the same thing whichever door it came through.
 *
 * <p>The status update, the history row and the audit row are one transaction,
 * and the candidate row is locked first. The history trigger is the final
 * word on legality: if this code and allowed_transitions ever disagree, the
 * database refuses and the request fails, rather than a status nobody
 * sanctioned being written.
 */
@Service
public class DecisionService {
    /** The engine's own marker for a person's rejection; its reconsider logic keys on it. */
    static final String HUMAN_PREFIX = "human rejection: ";

    private final JdbcClient db;
    private final Operator operator;

    public DecisionService(JdbcClient db, Operator operator) {
        this.db = db;
        this.operator = operator;
    }

    private record Locked(long id, String status, String rejectKind, String url) {}

    private Locked lock(String slug) {
        return db.sql("""
                SELECT id, status::text AS status, reject_kind::text AS reject_kind, url
                  FROM candidates WHERE slug = :slug FOR UPDATE
                """)
                .param("slug", slug)
                .query((rs, n) -> new Locked(rs.getLong("id"), rs.getString("status"),
                        rs.getString("reject_kind"), rs.getString("url")))
                .optional()
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND, "no candidate " + slug));
    }

    @Transactional
    public Decision approve(String slug, String note) {
        Locked c = lock(slug);
        if (!"proposed".equals(c.status())) {
            // Usually a second tap after the first one landed. Say which it was
            // rather than succeed silently.
            throw new ResponseStatusException(HttpStatus.CONFLICT,
                    slug + " is " + c.status() + ", not awaiting approval");
        }
        String historyNote = "human approval" + (blank(note) ? "" : ": " + note.strip());
        move(c, "approved", historyNote);
        db.sql("UPDATE candidates SET status = 'approved', updated_at = now() WHERE id = :id")
                .param("id", c.id()).update();
        audit("approve", slug, c.url());
        return new Decision(slug, "approved", true, "approved " + slug);
    }

    @Transactional
    public Decision reject(String slug, String reason) {
        if (blank(reason)) {
            // Without a reason a person's rejection is indistinguishable from
            // the scorer's, and only the scorer's comes back round.
            throw new ResponseStatusException(HttpStatus.BAD_REQUEST, "a rejection needs a reason");
        }
        String why = HUMAN_PREFIX + reason.strip();
        Locked c = lock(slug);
        switch (c.status()) {
            case "abandoned" -> {
                return new Decision(slug, c.status(), false, slug + " is already abandoned");
            }
            case "rejected" -> {
                if ("human".equals(c.rejectKind())) {
                    return new Decision(slug, c.status(), false, slug + " was already rejected by you");
                }
                // Rejected by the machine, which would reconsider it later.
                // Recording the person's reason over the top makes it final;
                // the status does not move, so there is no history edge.
                db.sql("""
                        UPDATE candidates SET reject_kind = 'human', reject_reason = :why,
                               rejected_at = now(), updated_at = now() WHERE id = :id
                        """).param("why", why).param("id", c.id()).update();
                audit("reject", slug, reason.strip());
                return new Decision(slug, c.status(), true, slug + " stays rejected, now on your reason");
            }
            default -> {
                move(c, "rejected", why);
                db.sql("""
                        UPDATE candidates SET status = 'rejected', reject_kind = 'human',
                               reject_reason = :why, rejected_at = now(), updated_at = now()
                         WHERE id = :id
                        """).param("why", why).param("id", c.id()).update();
                audit("reject", slug, reason.strip());
                return new Decision(slug, "rejected", true, "rejected " + slug);
            }
        }
    }

    /** Writes the history edge first, so an illegal one fails before anything else changes. */
    private void move(Locked c, String to, String note) {
        try {
            db.sql("""
                    INSERT INTO candidate_history (candidate_id, from_status, to_status, note, actor)
                    VALUES (:id, CAST(:from AS candidate_status), CAST(:to AS candidate_status), :note, :actor)
                    """)
                    .param("id", c.id()).param("from", c.status()).param("to", to)
                    .param("note", note).param("actor", operator.operator())
                    .update();
        } catch (DataAccessException e) {
            throw new ResponseStatusException(HttpStatus.CONFLICT,
                    "the state machine refuses " + c.status() + " -> " + to, e);
        }
    }

    private void audit(String action, String slug, String detail) {
        db.sql("INSERT INTO audit (action, slug, detail, actor) VALUES (:a, :s, :d, :actor)")
                .param("a", action).param("s", slug).param("d", detail)
                .param("actor", operator.operator() + " (dashboard)")
                .update();
    }

    private static boolean blank(String s) {
        return s == null || s.isBlank();
    }
}
