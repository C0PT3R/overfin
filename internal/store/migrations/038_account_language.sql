-- The language of each account's site, titles and emails, among those the
-- operator offers; NULL until it's chosen (the default applies).
ALTER TABLE accounts ADD COLUMN language TEXT;
