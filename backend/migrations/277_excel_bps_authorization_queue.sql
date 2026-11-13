-- Local password/TOTP automation for independent Excel grants. Disabled by default.
CREATE TABLE openai_excel_login_configs (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 login_email TEXT NOT NULL,
 password_ciphertext TEXT NOT NULL,
 totp_ciphertext TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE openai_excel_login_tasks (
 id BIGSERIAL PRIMARY KEY,
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 config_revision BIGINT NOT NULL,
 expected_credentials_hash TEXT NOT NULL,
 expected_grant_hash TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued',
 stage TEXT NOT NULL DEFAULT 'queued',
 worker_id TEXT NOT NULL DEFAULT '',
 error_code TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX openai_excel_login_one_active ON openai_excel_login_tasks(account_id)
 WHERE status IN ('queued','running');
CREATE INDEX openai_excel_login_queue ON openai_excel_login_tasks(status,created_at);
