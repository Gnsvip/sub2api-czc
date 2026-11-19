-- Keep fixed failure codes intact: automatic retry policy reads error_code.
-- Diagnostics are bounded, allowlisted snapshots, not browser output or secrets.
ALTER TABLE openai_excel_login_tasks
 ADD COLUMN diagnostic TEXT NOT NULL DEFAULT '';
