-- State is separate from administrator-editable account metadata.
CREATE TABLE openai_excel_bps_auto_states (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 state JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
