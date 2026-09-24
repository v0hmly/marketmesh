-- Existing account preferences stay unchanged. New accounts require an email code.
ALTER TABLE auth.account_security ALTER COLUMN login_code_enabled SET DEFAULT true;

-- Additive: old challenges remain confirmation-only; old writers may omit it.
ALTER TABLE auth.security_challenges ADD COLUMN registration_digest bytea
    CHECK (registration_digest IS NULL OR
           (purpose = 'verify' AND octet_length(registration_digest) = 32));
