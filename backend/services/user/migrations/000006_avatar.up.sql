ALTER TABLE users.profiles
 ADD COLUMN avatar_file_id bytea CHECK (avatar_file_id IS NULL OR (octet_length(avatar_file_id)=16 AND avatar_file_id<>decode(repeat('00',16),'hex'))),
 ADD COLUMN avatar_version bigint NOT NULL DEFAULT 1 CHECK (avatar_version>0);

CREATE TABLE users.avatar_retirements (
 subject_id bytea NOT NULL REFERENCES users.profiles(subject_id) ON DELETE CASCADE,
 file_id bytea NOT NULL CHECK (octet_length(file_id)=16 AND file_id<>decode(repeat('00',16),'hex')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
 lease_token bytea CHECK (lease_token IS NULL OR octet_length(lease_token)=16),
 lease_until timestamptz,
 retired_at timestamptz,
 PRIMARY KEY(subject_id,file_id),
 CHECK ((lease_token IS NULL)=(lease_until IS NULL))
);
CREATE INDEX avatar_retirements_pending ON users.avatar_retirements(next_attempt_at,created_at) WHERE retired_at IS NULL;
