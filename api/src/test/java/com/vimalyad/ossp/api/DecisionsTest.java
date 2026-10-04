package com.vimalyad.ossp.api;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.MediaType;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.servlet.MockMvc;

/**
 * The human gate against a real Postgres with the real migrations applied.
 *
 * <p>Skipped without OSSP_TEST_JDBC_URL, the same way the engine's pg tests
 * are, so the suite stays green on a machine with no database. Most of what is
 * asserted here is the schema's behaviour rather than this code's -- the
 * trigger refusing an edge, the constraint demanding a reason -- because that
 * is where the rules live.
 */
@SpringBootTest(properties = "ossp.operator=tester")
@AutoConfigureMockMvc
@EnabledIfEnvironmentVariable(named = "OSSP_TEST_JDBC_URL", matches = ".+")
class DecisionsTest {
    @DynamicPropertySource
    static void datasource(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", () -> System.getenv("OSSP_TEST_JDBC_URL"));
        r.add("spring.datasource.username", () -> env("OSSP_TEST_DB_USER", "ossp"));
        r.add("spring.datasource.password", () -> env("OSSP_TEST_DB_PASSWORD", "dev"));
    }

    private static String env(String k, String fallback) {
        String v = System.getenv(k);
        return v == null ? fallback : v;
    }

    @Autowired MockMvc mvc;
    @Autowired JdbcClient db;

    /** A fresh candidate walked legally to {@code status}, so tests never share rows. */
    private String candidate(String... path) {
        String slug = "it__" + UUID.randomUUID().toString().substring(0, 8);
        db.sql("""
                INSERT INTO repos (full_name, owner) VALUES ('it-org/it-repo', 'it-org')
                ON CONFLICT (full_name) DO NOTHING
                """).update();
        long id = db.sql("""
                INSERT INTO candidates (repo_id, issue_number, slug, title, url, status)
                SELECT id, (random() * 1e9)::int, :slug, 'test', 'https://example.invalid/' || :slug,
                       CAST(:first AS candidate_status)
                  FROM repos WHERE full_name = 'it-org/it-repo'
                RETURNING id
                """).param("slug", slug).param("first", path[0]).query(Long.class).single();
        db.sql("INSERT INTO candidate_history (candidate_id, to_status) VALUES (:id, CAST(:s AS candidate_status))")
                .param("id", id).param("s", path[0]).update();
        for (int i = 1; i < path.length; i++) {
            db.sql("""
                    INSERT INTO candidate_history (candidate_id, from_status, to_status)
                    VALUES (:id, CAST(:f AS candidate_status), CAST(:t AS candidate_status))
                    """).param("id", id).param("f", path[i - 1]).param("t", path[i]).update();
        }
        // A machine rejection, if that is where the path ends: the constraint
        // refuses a rejected row that does not say why.
        db.sql("""
                UPDATE candidates SET status = CAST(:s AS candidate_status),
                       reject_kind = CASE WHEN :s = 'rejected' THEN 'quality'::rejection_kind END,
                       reject_reason = CASE WHEN :s = 'rejected' THEN 'failed a bar' END
                 WHERE id = :id
                """).param("s", path[path.length - 1]).param("id", id).update();
        return slug;
    }

    private String statusOf(String slug) {
        return db.sql("SELECT status::text FROM candidates WHERE slug = :s").param("s", slug)
                .query(String.class).single();
    }

    @Test
    void approveWritesStatusHistoryAndAuditTogether() throws Exception {
        String slug = candidate("discovered", "scored", "proposed");

        mvc.perform(post("/api/candidates/{s}/approve", slug))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("approved"))
                .andExpect(jsonPath("$.changed").value(true));

        assertThat(statusOf(slug)).isEqualTo("approved");
        assertThat(db.sql("""
                SELECT actor FROM candidate_history h JOIN candidates c ON c.id = h.candidate_id
                 WHERE c.slug = :s AND h.to_status = 'approved'
                """).param("s", slug).query(String.class).single()).isEqualTo("tester");
        assertThat(db.sql("SELECT count(*) FROM audit WHERE slug = :s AND action = 'approve'")
                .param("s", slug).query(Integer.class).single()).isEqualTo(1);
    }

    @Test
    void secondApproveIsAConflictNotASilentSuccess() throws Exception {
        String slug = candidate("discovered", "scored", "proposed");
        mvc.perform(post("/api/candidates/{s}/approve", slug)).andExpect(status().isOk());
        mvc.perform(post("/api/candidates/{s}/approve", slug))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.detail").value(slug + " is approved, not awaiting approval"));
    }

    @Test
    void rejectNeedsAReason() throws Exception {
        String slug = candidate("discovered", "scored", "proposed");
        mvc.perform(post("/api/candidates/{s}/reject", slug)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"reason\":\"  \"}"))
                .andExpect(status().isBadRequest());
        assertThat(statusOf(slug)).isEqualTo("proposed");
    }

    @Test
    void rejectRecordsAHumanRejectionTheEngineWillNeverReconsider() throws Exception {
        String slug = candidate("discovered", "scored", "proposed");
        mvc.perform(post("/api/candidates/{s}/reject", slug)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"reason\":\"out of scope\"}"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("rejected"));
        assertThat(db.sql("SELECT reject_kind::text || '|' || reject_reason FROM candidates WHERE slug = :s")
                .param("s", slug).query(String.class).single())
                .isEqualTo("human|human rejection: out of scope");
    }

    @Test
    void rejectingAMachineRejectionMakesItFinalWithoutANewEdge() throws Exception {
        String slug = candidate("discovered", "rejected");
        int edges = db.sql("SELECT count(*) FROM candidate_history h JOIN candidates c ON c.id = h.candidate_id WHERE c.slug = :s")
                .param("s", slug).query(Integer.class).single();

        mvc.perform(post("/api/candidates/{s}/reject", slug)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"reason\":\"not for me\"}"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.changed").value(true));

        assertThat(db.sql("SELECT reject_kind::text FROM candidates WHERE slug = :s")
                .param("s", slug).query(String.class).single()).isEqualTo("human");
        assertThat(db.sql("SELECT count(*) FROM candidate_history h JOIN candidates c ON c.id = h.candidate_id WHERE c.slug = :s")
                .param("s", slug).query(Integer.class).single()).isEqualTo(edges);
    }

    @Test
    void theDatabaseRefusesAnEdgeTheTableDoesNotHave() throws Exception {
        // pr_open -> rejected is not in allowed_transitions. This code does not
        // check it; the trigger does, and nothing else may change.
        String slug = candidate("discovered", "scored", "proposed", "approved", "implementing",
                "implemented", "pushed", "pr_open");
        mvc.perform(post("/api/candidates/{s}/reject", slug)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"reason\":\"changed my mind\"}"))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.detail").value("the state machine refuses pr_open -> rejected"));
        assertThat(statusOf(slug)).isEqualTo("pr_open");
        assertThat(db.sql("SELECT count(*) FROM audit WHERE slug = :s").param("s", slug)
                .query(Integer.class).single()).isZero();
    }

    @Test
    void readModelsAnswer() throws Exception {
        mvc.perform(get("/api/overview")).andExpect(status().isOk()).andExpect(jsonPath("$.caps.prsPerWeek").isNumber());
        mvc.perform(get("/api/prs?all=true")).andExpect(status().isOk());
        mvc.perform(get("/api/needs-you")).andExpect(status().isOk());
        mvc.perform(get("/api/throughput")).andExpect(status().isOk());
        mvc.perform(get("/api/funnel")).andExpect(status().isOk());
        mvc.perform(get("/api/candidates?status=nonsense")).andExpect(status().isBadRequest());
        mvc.perform(get("/api/candidates/does-not-exist")).andExpect(status().isNotFound());
        mvc.perform(get("/api/nope")).andExpect(status().isNotFound());
    }
}
