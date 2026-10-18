-- +goose Up
-- When a source row's peer counts were observed. updated_at cannot serve: it also moves
-- on writes that keep the old counts. Existing rows are not backfilled, so their counts
-- stay undated until they are scraped again.
ALTER TABLE torrents_torrent_sources ADD COLUMN scraped_at timestamptz;

-- +goose Down
ALTER TABLE torrents_torrent_sources DROP COLUMN scraped_at;
