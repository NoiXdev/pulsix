-- +goose Up
-- Per-user appearance preferences (backlog 3.97–3.100, spec docs/superpowers/
-- specs/2026-11-17-lists-and-appearance-design.md): the list density, the page
-- size of every list and, per list id, the columns the user chose. A user
-- without a row has the defaults. lists is one document keyed by list id; the
-- service bounds its size and shape.
CREATE TABLE user_preferences (
    user_id    uuid PRIMARY KEY REFERENCES users ON DELETE CASCADE,
    density    text NOT NULL DEFAULT 'standard' CHECK (density IN ('comfortable', 'standard', 'compact')),
    page_size  int  NOT NULL DEFAULT 25 CHECK (page_size IN (10, 25, 50, 100)),
    lists      jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_preferences;
