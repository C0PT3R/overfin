-- Set by the operator in the control panel.
ALTER TABLE accounts ADD COLUMN label TEXT;          -- a note to tell accounts apart
ALTER TABLE accounts ADD COLUMN expires_at INTEGER;  -- access ends then; NULL: never
ALTER TABLE accounts ADD COLUMN max_streams INTEGER; -- overrides the instance's limit; NULL: the instance's
