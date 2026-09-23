-- Stop recovery-code traffic before downgrade. Codes cannot be restored.
DROP TABLE auth.recovery_codes;
DELETE FROM auth.security_challenges WHERE purpose = 'recovery_codes';
ALTER TABLE auth.security_challenges DROP CONSTRAINT security_challenges_purpose_check;
ALTER TABLE auth.security_challenges ADD CONSTRAINT security_challenges_purpose_check
    CHECK (purpose IN ('login','verify','reset','change_email','cancel_email','cancel_deletion','enable_code','disable_code'));
