CREATE SCHEMA IF NOT EXISTS files;

CREATE TABLE files.uploads (
    id bytea PRIMARY KEY CHECK (octet_length(id) = 16),
    tenant_id bytea NOT NULL CHECK (octet_length(tenant_id) = 16),
    owner_id bytea NOT NULL CHECK (octet_length(owner_id) = 16),
    idempotency_key bytea NOT NULL CHECK (octet_length(idempotency_key) = 16),
    object_key text NOT NULL UNIQUE CHECK (object_key ~ '^[0-9a-f]{32}$'),
    manifest jsonb NOT NULL CHECK (octet_length(manifest::text) <= 8192),
    manifest_hash bytea NOT NULL CHECK (octet_length(manifest_hash) = 32),
    upload_id text NOT NULL DEFAULT '' CHECK (octet_length(upload_id) <= 1024),
    state text NOT NULL CHECK (state IN ('UPLOADING','SCANNING','REPLICATING','READY','REJECTED','EXPIRED','DELETED')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    clean_format text NOT NULL DEFAULT '',
    clean_size bigint NOT NULL DEFAULT 0 CHECK (clean_size BETWEEN 0 AND 104857600),
    clean_sha256 bytea NOT NULL DEFAULT '',
    lease_until timestamptz,
    cleanup_after timestamptz,
    UNIQUE (tenant_id, owner_id, idempotency_key),
    CHECK (state <> 'READY' OR (clean_size > 0 AND octet_length(clean_sha256) = 32 AND clean_format IN ('image/png','image/jpeg','application/pdf')))
);
CREATE INDEX uploads_work ON files.uploads (state, lease_until, updated_at);
CREATE INDEX uploads_owner_created ON files.uploads (tenant_id, owner_id, created_at);

-- Serialize quota checks for one owner. Idempotent retries do not consume quota.
CREATE FUNCTION files.check_upload_quota() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(encode(NEW.tenant_id,'hex') || encode(NEW.owner_id,'hex'), 43));
    IF EXISTS (SELECT 1 FROM files.uploads WHERE tenant_id=NEW.tenant_id AND owner_id=NEW.owner_id AND idempotency_key=NEW.idempotency_key) THEN
        RETURN NEW;
    END IF;
    IF (SELECT count(*) FROM files.uploads WHERE tenant_id=NEW.tenant_id AND owner_id=NEW.owner_id AND state IN ('UPLOADING','SCANNING','REPLICATING')) >= 10
       OR (SELECT count(*) FROM files.uploads WHERE tenant_id=NEW.tenant_id AND owner_id=NEW.owner_id AND created_at > clock_timestamp()-interval '24 hours') >= 100 THEN
        RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='file quota reached';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER file_upload_quota BEFORE INSERT ON files.uploads
FOR EACH ROW EXECUTE FUNCTION files.check_upload_quota();

-- No content, filenames, capabilities, credentials or raw scanner output in audit.
CREATE TABLE files.audit (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_id bytea NOT NULL,
    state text NOT NULL,
    version bigint NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION files.audit_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR OLD.state IS DISTINCT FROM NEW.state THEN
        INSERT INTO files.audit(file_id,state,version) VALUES(NEW.id,NEW.state,NEW.version);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER file_state_audit AFTER INSERT OR UPDATE OF state ON files.uploads
FOR EACH ROW EXECUTE FUNCTION files.audit_transition();
