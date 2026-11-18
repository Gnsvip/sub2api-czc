-- Ordinary Codex login credentials stay separate from independent Excel grants.
-- Reuse the worker queue so task IDs and account-level login ownership are unique.
CREATE TABLE openai_codex_login_configs (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 login_email TEXT NOT NULL,
 password_ciphertext TEXT NOT NULL,
 totp_ciphertext TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE openai_excel_login_tasks
 ADD COLUMN oauth_profile TEXT NOT NULL DEFAULT 'excel' CHECK (oauth_profile IN ('excel','codex')),
 ADD COLUMN expected_account_state JSONB NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX openai_codex_login_history ON openai_excel_login_tasks(account_id,id DESC)
 WHERE oauth_profile='codex';
