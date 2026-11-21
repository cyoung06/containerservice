-- SSH public keys: the ones the bastion accepts and every container's authorized_keys.
-- id is the hex SHA-256 of the key's wire form, so a key is listed once.
CREATE TABLE ssh_keys (
    id         text PRIMARY KEY,
    line       text NOT NULL,
    label      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
