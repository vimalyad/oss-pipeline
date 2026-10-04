package com.vimalyad.ossp.api.candidates;

import com.vimalyad.ossp.api.candidates.Models.CandidateDetail;
import com.vimalyad.ossp.api.candidates.Models.CandidateSummary;
import com.vimalyad.ossp.api.candidates.Models.Decision;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import java.util.List;
import java.util.Set;
import org.springframework.http.HttpStatus;
import org.springframework.validation.annotation.Validated;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;

@RestController
@RequestMapping("/api/candidates")
@Validated
class CandidateController {
    /** Mirrors the candidate_status enum, so a typo is a 400 rather than an empty list. */
    static final Set<String> STATUSES = Set.of(
            "discovered", "scored", "proposed", "approved", "auto_approved", "rejected",
            "implementing", "implemented", "abandoned", "pushed", "pr_open",
            "changes_requested", "updating", "merged", "closed", "stale");

    /** Everything still in motion; rejected and finished work is asked for by name. */
    static final List<String> ACTIVE = List.of(
            "proposed", "approved", "auto_approved", "implementing", "implemented",
            "pushed", "pr_open", "changes_requested", "updating", "stale");

    private final CandidateRepository repo;
    private final DecisionService decisions;

    CandidateController(CandidateRepository repo, DecisionService decisions) {
        this.repo = repo;
        this.decisions = decisions;
    }

    @GetMapping
    List<CandidateSummary> list(
            @RequestParam(required = false) List<String> status,
            @RequestParam(defaultValue = "200") @Min(1) @Max(1000) int limit) {
        List<String> wanted = status == null || status.isEmpty() ? ACTIVE : status;
        for (String s : wanted) {
            if (!STATUSES.contains(s)) {
                throw new ResponseStatusException(HttpStatus.BAD_REQUEST, "unknown status " + s);
            }
        }
        return repo.byStatus(wanted, limit);
    }

    @GetMapping("/{slug}")
    CandidateDetail get(@PathVariable String slug) {
        CandidateSummary c = repo.bySlug(slug)
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND, "no candidate " + slug));
        return new CandidateDetail(c, repo.brief(c.id()).orElse(null), repo.signal(c.id()).orElse(null),
                repo.history(c.id()), repo.audit(slug, 100));
    }

    record ApproveRequest(String note) {}

    record RejectRequest(@NotBlank String reason) {}

    @PostMapping("/{slug}/approve")
    Decision approve(@PathVariable String slug, @RequestBody(required = false) ApproveRequest body) {
        return decisions.approve(slug, body == null ? null : body.note());
    }

    @PostMapping("/{slug}/reject")
    Decision reject(@PathVariable String slug, @RequestBody @Validated RejectRequest body) {
        return decisions.reject(slug, body.reason());
    }
}
