-- Additive: old instances ignore recovery codes and keep enforcing email 2FA.
ALTER TABLE auth.security_challenges DROP CONSTRAINT security_challenges_purpose_check;
ALTER TABLE auth.security_challenges ADD CONSTRAINT security_challenges_purpose_check
    CHECK (purpose IN ('login','verify','reset','change_email','cancel_email','cancel_deletion','enable_code','disable_code','recovery_codes'));

-- At most eight entries per account; replacement removes the previous set.
-- Revision makes credential/policy changes revoke the entire set immediately.
CREATE TABLE auth.recovery_codes (
    subject_id bytea NOT NULL REFERENCES auth.credentials(subject_id),
    secret_digest bytea NOT NULL CHECK (octet_length(secret_digest) = 32),
    revision bigint NOT NULL CHECK (revision > 0),
    PRIMARY KEY (subject_id, secret_digest)
);
