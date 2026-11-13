-- Keep Excel authorization separate from primary Codex credentials.
CREATE TABLE IF NOT EXISTS openai_excel_oauth_credentials (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 credentials_ciphertext TEXT NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
