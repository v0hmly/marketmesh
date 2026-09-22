-- Destructive: only for disposable fixtures or separately approved data removal.
DROP TABLE users.avatar_retirements;
ALTER TABLE users.profiles DROP COLUMN avatar_file_id, DROP COLUMN avatar_version;
