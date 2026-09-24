CREATE SCHEMA staff;
CREATE TABLE staff.login_attempts (
 state_hash text PRIMARY KEY,
 browser_hash text NOT NULL,
 nonce text NOT NULL,
 verifier text NOT NULL,
 invite text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX ON staff.login_attempts(expires_at);
CREATE TABLE staff.sessions (
 token_hash text PRIMARY KEY,
 issuer text NOT NULL,
 subject text NOT NULL,
 email text NOT NULL,
 display_name text NOT NULL,
 expires_at timestamptz NOT NULL,
 idle_until timestamptz NOT NULL
);
CREATE INDEX ON staff.sessions(expires_at);
CREATE TABLE staff.members (
 issuer text NOT NULL,
 subject text NOT NULL,
 role text NOT NULL CHECK (role IN ('support','moderator','admin')),
 PRIMARY KEY(issuer,subject)
);
CREATE TABLE staff.invites (
 token_hash text PRIMARY KEY,
 email text NOT NULL,
 role text NOT NULL CHECK (role IN ('support','moderator','admin')),
 inviter_name text NOT NULL,
 expires_at timestamptz NOT NULL,
 used_at timestamptz
);
