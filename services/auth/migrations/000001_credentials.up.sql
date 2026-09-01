CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE auth.credentials (
    subject_id bytea PRIMARY KEY CHECK (octet_length(subject_id) = 16),
    identifier text NOT NULL UNIQUE CHECK (octet_length(identifier) BETWEEN 3 AND 254),
    password_digest text NOT NULL CHECK (octet_length(password_digest) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
