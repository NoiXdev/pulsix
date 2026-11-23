-- +goose Up
-- A repository playbook's text as of the last sync (backlog 3.127): the
-- worker ships each reported playbook's file beside the variable sources,
-- and the finish stores it with the path's row, in the same write that
-- replaces the rows. content_left_out is why a sync sent no text (too_large,
-- budget, not_utf8, unreadable). Both NULL: the sync sent neither (a worker
-- older than the texts), and for every existing row.
ALTER TABLE repo_playbook_sources ADD COLUMN content text, ADD COLUMN content_left_out text;

-- +goose Down
ALTER TABLE repo_playbook_sources DROP COLUMN content, DROP COLUMN content_left_out;
