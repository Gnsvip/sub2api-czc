-- Nullable request-time Standard pricing snapshot. Do not backfill from current group settings.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS free_fast_cost JSONB;
COMMENT ON COLUMN usage_logs.free_fast_cost IS 'Pre-multiplier customer cost details when Free Fast was applied; NULL for legacy and ordinary requests';
