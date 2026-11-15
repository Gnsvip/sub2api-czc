-- Keep penalty decisions separate from removable audit/evidence records.
ALTER TABLE users ADD COLUMN IF NOT EXISTS content_moderation_reset_at timestamptz;

-- Preserve the previous counter boundary for already existing users. Future
-- unbans use the actual unban time rather than the time of the previous ban.
UPDATE users u SET content_moderation_reset_at = b.at
FROM (SELECT user_id, MAX(created_at) AS at FROM content_moderation_logs
      WHERE auto_banned = TRUE GROUP BY user_id) b
WHERE u.id = b.user_id AND u.content_moderation_reset_at IS NULL;

INSERT INTO settings (key, value)
VALUES ('content_moderation_penalty_started_at', clock_timestamp()::text)
ON CONFLICT (key) DO NOTHING;

CREATE OR REPLACE FUNCTION reset_content_moderation_on_unban() RETURNS trigger AS $$
BEGIN
    IF OLD.status IS DISTINCT FROM 'active' AND NEW.status = 'active' THEN
        NEW.content_moderation_reset_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_reset_content_moderation_on_unban ON users;
CREATE TRIGGER users_reset_content_moderation_on_unban
BEFORE UPDATE OF status ON users FOR EACH ROW
EXECUTE FUNCTION reset_content_moderation_on_unban();

CREATE TABLE IF NOT EXISTS content_moderation_penalty_events (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_key text NOT NULL,
    occurred_at timestamptz NOT NULL,
    cyber boolean NOT NULL,
    counted boolean NOT NULL,
    PRIMARY KEY (user_id, event_key)
);
CREATE INDEX IF NOT EXISTS idx_moderation_penalty_events_time
    ON content_moderation_penalty_events (occurred_at);
