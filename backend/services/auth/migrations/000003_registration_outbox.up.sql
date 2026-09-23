-- Событие регистрации и credential фиксируются одной локальной транзакцией.
CREATE TABLE auth.registration_outbox (
    event_id bytea PRIMARY KEY CHECK (octet_length(event_id) = 16 AND event_id <> decode(repeat('00', 16), 'hex')),
    subject_id bytea NOT NULL UNIQUE CHECK (octet_length(subject_id) = 16 AND subject_id <> decode(repeat('00', 16), 'hex')),
    occurred_at timestamptz NOT NULL,
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 8192),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_token bytea CHECK (octet_length(lease_token) = 16 AND lease_token <> decode(repeat('00', 16), 'hex')),
    lease_until timestamptz,
    published_at timestamptz,
    CHECK ((lease_token IS NULL) = (lease_until IS NULL))
);
CREATE INDEX registration_outbox_pending_idx ON auth.registration_outbox(next_attempt_at, occurred_at, event_id) WHERE published_at IS NULL;
