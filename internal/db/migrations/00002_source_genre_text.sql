-- +goose Up
-- Raw Genre text of a logged-in F95 detail fetch. Only primary F95 Sources carry it; the
-- non-primary CHECK in 00001 cannot be altered, so writers clear it (DemoteSource).
ALTER TABLE source ADD COLUMN genre_text TEXT;

-- +goose Down
ALTER TABLE source DROP COLUMN genre_text;
