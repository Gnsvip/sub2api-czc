-- Google platforms are supported again. Keep all existing records and states.
-- This replacement prevents destructive retirement on first upgrade;
-- migration 274 reconciles constraints for databases that already ran retirement.
SELECT 1;
