ALTER TABLE users.profiles
    ADD COLUMN theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark')),
    ADD COLUMN settings_version bigint NOT NULL DEFAULT 1 CHECK (settings_version >= 1);
