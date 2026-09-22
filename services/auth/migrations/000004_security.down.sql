-- Stop security-enabled traffic and invalidate all live sessions before rollback.
DROP TABLE auth.mail_outbox;
DROP TABLE auth.login_limits;
DROP TABLE auth.security_challenges;
DROP TABLE auth.account_security;
