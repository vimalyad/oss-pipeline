package com.vimalyad.ossp.api.board;

import com.vimalyad.ossp.api.board.PrRepository.Feedback;
import com.vimalyad.ossp.api.board.PrRepository.PrCard;
import com.vimalyad.ossp.api.board.PrRepository.StateChange;
import com.vimalyad.ossp.api.candidates.CandidateRepository;
import com.vimalyad.ossp.api.candidates.Models.CandidateSummary;
import com.vimalyad.ossp.api.candidates.Models.HistoryEntry;
import java.util.List;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;

@RestController
@RequestMapping("/api/prs")
class PrController {
    /**
     * Everything one pull request page needs, in one response. The PR's own
     * state changes and the candidate's lifecycle are two different clocks;
     * the frontend interleaves them into one timeline.
     */
    record PrDetail(
            PrCard pr,
            CandidateSummary candidate,
            List<StateChange> states,
            List<HistoryEntry> lifecycle,
            List<Feedback> feedback) {}

    private final PrRepository prs;
    private final CandidateRepository candidates;

    PrController(PrRepository prs, CandidateRepository candidates) {
        this.prs = prs;
        this.candidates = candidates;
    }

    @GetMapping
    List<PrCard> list(@RequestParam(defaultValue = "false") boolean all) {
        return prs.list(!all);
    }

    @GetMapping("/{id}")
    PrDetail get(@PathVariable long id) {
        PrCard pr = prs.byId(id)
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND, "no pull request " + id));
        long candidateId = prs.candidateId(id);
        return new PrDetail(pr, candidates.bySlug(pr.slug()).orElse(null), prs.states(id),
                candidates.history(candidateId), prs.feedback(id));
    }
}
