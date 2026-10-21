-- The module stores the API's own item IDs ("item_…"), which stay valid
-- when Overfin serves several backends; rows from before held bare ones.
UPDATE watchlist SET item_id = 'item_' || item_id WHERE item_id NOT LIKE 'item\_%' ESCAPE '\';
