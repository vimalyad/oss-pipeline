package com.vimalyad.ossp.api.overview;

import static com.vimalyad.ossp.api.Rows.ts;

import com.vimalyad.ossp.api.candidates.CandidateRepository;
import com.vimalyad.ossp.api.candidates.Models.AuditEntry;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import java.time.LocalDate;
import java.time.OffsetDateTime;
import java.util.List;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.validation.annotation.Validated;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

/**
 * The home page: what the engine is obeying, how much of it is spent, and
 * what is waiting on a person.
 */
@RestController
@RequestMapping("/api")
@Validated
class OverviewController {
    record Caps(int prsPerDay, int prsPerWeek, int maxOpenPrs, int maxOpenPerRepo) {}

    record Usage(int openedToday, int openedThisWeek, int open) {}

    record Overview(Caps caps, Usage usage, int needsYou, int awaitingApproval, int mergedAllTime) {}

    record NeedsYou(String what, long refId, String repo, int issueNumber, String title, String link,
                    String author, OffsetDateTime since, String slug, Long prId) {}

    record Week(LocalDate week, int opened, int merged, int closed, int proposed, int approved) {}

    record Stage(String stage, int reached) {}

    /** The statuses claim_pr_slot counts as open. Kept identical so the meter and the cap agree. */
    private static final String OPEN = "('pushed','pr_open','changes_requested','updating','stale')";

    private final JdbcClient db;
    private final CandidateRepository candidates;

    OverviewController(JdbcClient db, CandidateRepository candidates) {
        this.db = db;
        this.candidates = candidates;
    }

    @GetMapping("/overview")
    Overview overview() {
        Caps caps = db.sql("SELECT prs_per_day, prs_per_week, max_open_prs, max_open_per_repo FROM caps WHERE id = 1")
                .query((rs, n) -> new Caps(rs.getInt(1), rs.getInt(2), rs.getInt(3), rs.getInt(4)))
                .single();
        // Counted from history exactly as claim_pr_slot counts it: a pull
        // request opened this morning and merged this afternoon still spent
        // today's budget.
        Usage usage = db.sql("""
                SELECT (SELECT count(DISTINCT candidate_id) FROM candidate_history
                         WHERE to_status = 'pr_open' AND at >= date_trunc('day', now())),
                       (SELECT count(DISTINCT candidate_id) FROM candidate_history
                         WHERE to_status = 'pr_open' AND at >= now() - INTERVAL '7 days'),
                       (SELECT count(*) FROM candidates WHERE status IN %s)
                """.formatted(OPEN))
                .query((rs, n) -> new Usage(rs.getInt(1), rs.getInt(2), rs.getInt(3)))
                .single();
        return db.sql("""
                SELECT (SELECT count(*) FROM v_needs_human),
                       (SELECT count(*) FROM candidates WHERE status = 'proposed'),
                       (SELECT count(*) FROM candidates WHERE status = 'merged')
                """)
                .query((rs, n) -> new Overview(caps, usage, rs.getInt(1), rs.getInt(2), rs.getInt(3)))
                .single();
    }

    /**
     * v_needs_human's ref_id is a feedback id for a reply and a candidate id
     * otherwise; resolve both to something the frontend can link to.
     */
    @GetMapping("/needs-you")
    List<NeedsYou> needsYou() {
        return db.sql("""
                SELECT n.*,
                       COALESCE(cf.slug, cc.slug) AS slug,
                       f.pr_id
                  FROM v_needs_human n
                  LEFT JOIN feedback_items f ON n.what = 'reply' AND f.id = n.ref_id
                  LEFT JOIN pull_requests p ON p.id = f.pr_id
                  LEFT JOIN candidates cf ON cf.id = p.candidate_id
                  LEFT JOIN candidates cc ON n.what <> 'reply' AND cc.id = n.ref_id
                 ORDER BY n.rank, n.since
                """)
                .query((rs, n) -> {
                    long pr = rs.getLong("pr_id");
                    return new NeedsYou(rs.getString("what"), rs.getLong("ref_id"), rs.getString("repo"),
                            rs.getInt("issue_number"), rs.getString("title"), rs.getString("link"),
                            rs.getString("author"), ts(rs, "since"), rs.getString("slug"),
                            rs.wasNull() ? null : pr);
                })
                .list();
    }

    @GetMapping("/throughput")
    List<Week> throughput(@RequestParam(defaultValue = "12") @Min(1) @Max(104) int weeks) {
        return db.sql("SELECT * FROM v_weekly_throughput LIMIT :weeks")
                .param("weeks", weeks)
                .query((rs, n) -> new Week(rs.getObject("week", LocalDate.class), rs.getInt("opened"),
                        rs.getInt("merged"), rs.getInt("closed"), rs.getInt("proposed"), rs.getInt("approved")))
                .list();
    }

    /** In lifecycle order rather than by count, so the drop between stages reads left to right. */
    @GetMapping("/funnel")
    List<Stage> funnel() {
        return db.sql("""
                SELECT stage::text AS stage, reached FROM v_funnel
                 WHERE stage IN ('discovered','scored','proposed','approved','auto_approved',
                                 'implemented','pr_open','merged')
                 ORDER BY array_position(ARRAY['discovered','scored','proposed','approved',
                          'auto_approved','implemented','pr_open','merged']::candidate_status[], stage)
                """)
                .query((rs, n) -> new Stage(rs.getString("stage"), rs.getInt("reached")))
                .list();
    }

    @GetMapping("/audit")
    List<AuditEntry> audit(@RequestParam(defaultValue = "100") @Min(1) @Max(1000) int limit) {
        return candidates.audit(null, limit);
    }
}
