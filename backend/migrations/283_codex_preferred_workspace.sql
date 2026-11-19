-- Optional explicit first-login workspace; existing identity bindings keep precedence.
ALTER TABLE openai_codex_login_configs
 ADD COLUMN preferred_workspace_id TEXT NOT NULL DEFAULT '';
