-- Additive migration. Security-enabled instances must not be rolled back to a
-- password-only version: legacy Login cannot enforce the new email-code policy.
CREATE TABLE auth.account_security (
    subject_id bytea PRIMARY KEY REFERENCES auth.credentials(subject_id),
    email_verified boolean NOT NULL DEFAULT false,
    login_code_enabled boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    deletion_at timestamptz,
    deleted_at timestamptz
);

CREATE TABLE auth.security_challenges (
    challenge_id bytea PRIMARY KEY CHECK (octet_length(challenge_id) = 16),
    subject_id bytea NOT NULL REFERENCES auth.credentials(subject_id),
    purpose text NOT NULL CHECK (purpose IN ('login','verify','reset','change_email','cancel_email','cancel_deletion','enable_code','disable_code')),
    secret_digest bytea NOT NULL CHECK (octet_length(secret_digest) = 32),
    revision bigint NOT NULL CHECK (revision > 0),
    email text NOT NULL DEFAULT '' CHECK (octet_length(email) <= 254),
    expires_at timestamptz NOT NULL,
    sent_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    sends integer NOT NULL DEFAULT 1 CHECK (sends BETWEEN 1 AND 5),
    used_at timestamptz
);
CREATE INDEX security_challenges_owner_idx ON auth.security_challenges(subject_id,purpose);
CREATE INDEX security_challenges_expiry_idx ON auth.security_challenges(expires_at);

-- HMAC keys conceal unknown login identifiers from offline enumeration.
CREATE TABLE auth.login_limits (
    bucket bytea PRIMARY KEY CHECK (octet_length(bucket) = 32),
    attempts integer NOT NULL CHECK (attempts BETWEEN 0 AND 100),
    expires_at timestamptz NOT NULL
);
CREATE INDEX login_limits_expiry_idx ON auth.login_limits(expires_at);

-- Payload contains authenticated encryption of recipient, template and secrets.
-- It is cleared after successful delivery, permanent failure or expiration.
CREATE TABLE auth.mail_outbox (
    message_id bytea PRIMARY KEY CHECK (octet_length(message_id) = 16),
    subject_id bytea NOT NULL REFERENCES auth.credentials(subject_id),
    payload bytea,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 12),
    lease_token bytea CHECK (octet_length(lease_token) = 16),
    lease_until timestamptz,
    completed_at timestamptz,
    outcome text CHECK (outcome IN ('delivered','expired','rejected','exhausted')),
    CHECK (expires_at > created_at),
    CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CHECK ((completed_at IS NULL) = (outcome IS NULL))
);
CREATE INDEX mail_outbox_pending_idx ON auth.mail_outbox(available_at,created_at) WHERE completed_at IS NULL;
