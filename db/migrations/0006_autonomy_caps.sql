-- Autonomous approval is on: nobody approves proposals by hand. The engine
-- reads its caps from config/policy.yaml; this keeps the table the dashboard
-- shows in step with it.

BEGIN;

ALTER TABLE caps ADD COLUMN auto_prs_per_week INTEGER NOT NULL DEFAULT 0;

-- Ramped: the first pull requests on a project are the ones its maintainers
-- judge an account by.
UPDATE caps SET auto_prs_per_day = 2, auto_prs_per_week = 5, max_open_auto_prs = 10,
                updated_at = now(), updated_by = 'operator: autonomy on'
 WHERE id = 1;

COMMIT;
