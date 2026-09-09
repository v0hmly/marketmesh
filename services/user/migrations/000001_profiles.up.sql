CREATE SCHEMA users;
CREATE TABLE users.profiles (
    subject_id bytea PRIMARY KEY CHECK (octet_length(subject_id) = 16 AND subject_id <> decode(repeat('00', 16), 'hex')),
    display_name text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 80 AND octet_length(display_name) <= 320),
    bio text NOT NULL DEFAULT '' CHECK (char_length(bio) <= 1000 AND octet_length(bio) <= 4000),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
