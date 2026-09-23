CREATE TABLE users.registration_inbox (
    event_id bytea PRIMARY KEY CHECK (octet_length(event_id) = 16 AND event_id <> decode(repeat('00', 16), 'hex')),
    subject_id bytea NOT NULL CHECK (octet_length(subject_id) = 16 AND subject_id <> decode(repeat('00', 16), 'hex')),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    received_at timestamptz NOT NULL DEFAULT now()
);
