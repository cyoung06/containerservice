-- The owner's Claude Code login (a long-lived OAuth token from `claude setup-token`) and shared
-- settings (managed settings JSON), copied to the containers that ask for them. One row.
CREATE TABLE claude (
    id         int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    token      text NOT NULL DEFAULT '',
    token_at   timestamptz,
    settings   text NOT NULL DEFAULT '',
    settings_at timestamptz
);
INSERT INTO claude DEFAULT VALUES;
