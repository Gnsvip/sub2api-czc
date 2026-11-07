-- GN2API may already have recorded 242_drop_platform_check_constraints before
-- importing CZC's earlier 233/241 migrations, which recreate these checks.
-- Restore 242's intended final schema without changing migration history.
-- Platform validation remains in the application; monitor checks stay intact.
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
