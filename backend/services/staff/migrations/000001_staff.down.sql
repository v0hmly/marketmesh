-- Rollback destroys only staff identities, sessions and invitations. Stop staff first.
DROP SCHEMA staff CASCADE;
