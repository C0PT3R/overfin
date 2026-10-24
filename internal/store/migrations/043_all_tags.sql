-- The index kept 50 tags of a title, so a hidden one further on hid
-- nothing: the library is read again once, with all of them.
DELETE FROM meta WHERE name = 'catalog-state';
