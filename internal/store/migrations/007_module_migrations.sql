-- Which of each module's own migrations have run.
CREATE TABLE module_migrations (
    module  TEXT PRIMARY KEY,
    version INTEGER NOT NULL
) STRICT;
