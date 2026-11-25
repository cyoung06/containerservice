-- Identities the owner made: each is a service account of the containers' namespace (its name has a
-- random part, so a subject is never used again), worn by the containers the owner attaches it to.
-- Kubernetes holds the account itself; this is the record of what exists and what it's called.
CREATE TABLE identities (
    name            text PRIMARY KEY,
    service_account text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now()
);
