-- Stop new Auth instances before rollback; pending links revert to confirmation-only.
ALTER TABLE auth.security_challenges DROP COLUMN registration_digest;
ALTER TABLE auth.account_security ALTER COLUMN login_code_enabled SET DEFAULT false;
