-- Account deletion is soft deletion, so FK ON DELETE CASCADE does not run.
-- Remove saved password/TOTP configuration in the same transaction as deletion.
-- Keep account/usage history; completed login diagnostics contain no credentials.
CREATE OR REPLACE FUNCTION cleanup_deleted_account_login_configs()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM openai_codex_login_configs WHERE account_id = NEW.id;
    DELETE FROM openai_excel_login_configs WHERE account_id = NEW.id;
    UPDATE openai_excel_login_tasks
       SET status = 'cancelled', stage = 'cancelled', error_code = 'account_changed', updated_at = NOW()
     WHERE account_id = NEW.id AND status IN ('queued', 'running');
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS accounts_cleanup_login_configs_on_delete ON accounts;
CREATE TRIGGER accounts_cleanup_login_configs_on_delete
AFTER UPDATE OF deleted_at ON accounts
FOR EACH ROW WHEN (NEW.deleted_at IS NOT NULL)
EXECUTE FUNCTION cleanup_deleted_account_login_configs();

-- Backfill only accounts already deleted; disabled/paused live accounts retain
-- their configuration. These statements share the migration transaction.
DELETE FROM openai_codex_login_configs c USING accounts a
 WHERE c.account_id = a.id AND a.deleted_at IS NOT NULL;
DELETE FROM openai_excel_login_configs c USING accounts a
 WHERE c.account_id = a.id AND a.deleted_at IS NOT NULL;
UPDATE openai_excel_login_tasks t
   SET status = 'cancelled', stage = 'cancelled', error_code = 'account_changed', updated_at = NOW()
  FROM accounts a
 WHERE t.account_id = a.id AND a.deleted_at IS NOT NULL AND t.status IN ('queued', 'running');
