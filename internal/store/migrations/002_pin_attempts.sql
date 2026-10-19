-- Track wrong PINs: when the last one was, and until when the PIN is
-- locked. (pin_locked from 001 is kept, unused, rather than rebuilding the
-- table.)
ALTER TABLE profiles ADD COLUMN pin_failed_at INTEGER;
ALTER TABLE profiles ADD COLUMN pin_locked_until INTEGER;
