CREATE TABLE auth.sessions (
    session_id bytea PRIMARY KEY CHECK (octet_length(session_id) = 16),
    subject_id bytea NOT NULL REFERENCES auth.credentials(subject_id),
    version bigint NOT NULL CHECK (version > 0),
    refresh_digest bytea NOT NULL CHECK (octet_length(refresh_digest) = 32),
    created_at timestamptz NOT NULL,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (created_at < access_expires_at AND access_expires_at <= expires_at),
    CHECK (created_at < refresh_expires_at AND refresh_expires_at <= expires_at)
);
CREATE INDEX sessions_subject_id_idx ON auth.sessions(subject_id) WHERE revoked_at IS NULL;

-- История сохраняется до удаления семьи после абсолютного срока действия.
CREATE TABLE auth.consumed_refresh_digests (
    session_id bytea NOT NULL REFERENCES auth.sessions(session_id) ON DELETE CASCADE,
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    consumed_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, digest)
);

-- Transactional outbox: доставка может повторяться; event_id служит ключом дедупликации.
CREATE TABLE auth.session_revocation_outbox (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id bytea NOT NULL CHECK (octet_length(session_id) = 16),
    subject_id bytea NOT NULL CHECK (octet_length(subject_id) = 16),
    version bigint NOT NULL CHECK (version > 0),
    occurred_at timestamptz NOT NULL,
    reason text NOT NULL CHECK (reason IN ('logout', 'logout_all', 'refresh_reuse', 'credential_change', 'administrative', 'issuance_failed')),
    published_at timestamptz
);
CREATE INDEX session_revocation_outbox_pending_idx ON auth.session_revocation_outbox(event_id) WHERE published_at IS NULL;
