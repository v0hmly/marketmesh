// Package postgres stores durable Files metadata with owner-scoped CAS operations.
package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

const columns = `id,tenant_id,owner_id,idempotency_key,object_key,manifest,manifest_hash,upload_id,state,version,created_at,expires_at,clean_format,clean_size,clean_sha256`

type Repository struct{ rw platformpostgres.Executor }

func New(rw platformpostgres.Executor) (*Repository, error) {
	if rw == nil {
		return nil, errors.New("files postgres: RW executor required")
	}
	return &Repository{rw: rw}, nil
}

func (r *Repository) Ready(ctx context.Context) error {
	var standby string
	if err := r.rw.QueryRow(ctx, "SHOW synchronous_standby_names").Scan(&standby); err != nil || standby == "" {
		return file.ErrUnavailable
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, v file.Record) (file.Record, error) {
	if !v.Owner.Valid() || v.ID == (file.ID{}) || v.IdempotencyKey == (file.ID{}) || v.Manifest.Validate() != nil {
		return file.Record{}, file.ErrInvalid
	}
	manifest, err := json.Marshal(v.Manifest)
	if err != nil {
		return file.Record{}, file.ErrInvalid
	}
	hash := v.Manifest.Fingerprint()
	created, err := decode(r.rw.QueryRow(ctx, `INSERT INTO files.uploads(id,tenant_id,owner_id,idempotency_key,object_key,manifest,manifest_hash,state,created_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,'UPLOADING',$8,$9) ON CONFLICT(tenant_id,owner_id,idempotency_key) DO NOTHING RETURNING `+columns, v.ID[:], v.Owner.Tenant[:], v.Owner.Subject[:], v.IdempotencyKey[:], v.ObjectKey, manifest, hash[:], v.CreatedAt, v.ExpiresAt))
	if !errors.Is(err, file.ErrNotFound) {
		return created, err
	}
	return decode(r.rw.QueryRow(ctx, `SELECT `+columns+` FROM files.uploads WHERE tenant_id=$1 AND owner_id=$2 AND idempotency_key=$3`, v.Owner.Tenant[:], v.Owner.Subject[:], v.IdempotencyKey[:]))
}

func (r *Repository) Get(ctx context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	if !owner.Valid() || id == (file.ID{}) {
		return file.Record{}, file.ErrInvalid
	}
	return decode(r.rw.QueryRow(ctx, `SELECT `+columns+` FROM files.uploads WHERE tenant_id=$1 AND owner_id=$2 AND id=$3`, owner.Tenant[:], owner.Subject[:], id[:]))
}

func (r *Repository) AttachUpload(ctx context.Context, v file.Record, uploadID string) (file.Record, error) {
	if !validVersion(v) || uploadID == "" || len(uploadID) > 1024 {
		return file.Record{}, file.ErrInvalid
	}
	result, err := decode(r.rw.QueryRow(ctx, `UPDATE files.uploads SET upload_id=$5,version=version+1,updated_at=clock_timestamp()
WHERE tenant_id=$1 AND owner_id=$2 AND id=$3 AND version=$4 AND state='UPLOADING' AND upload_id='' AND expires_at>clock_timestamp() RETURNING `+columns, v.Owner.Tenant[:], v.Owner.Subject[:], v.ID[:], v.Version, uploadID))
	if errors.Is(err, file.ErrNotFound) {
		return file.Record{}, file.ErrConflict
	}
	return result, err
}

func (r *Repository) Transition(ctx context.Context, v file.Record, to file.State) (file.Record, error) {
	if !validVersion(v) || !file.CanTransition(v.State, to) {
		return file.Record{}, file.ErrConflict
	}
	result, err := decode(r.rw.QueryRow(ctx, `UPDATE files.uploads SET state=$6,version=version+1,updated_at=clock_timestamp(),lease_until=NULL,
cleanup_after=CASE WHEN $6 IN ('DELETED','EXPIRED','REJECTED') THEN clock_timestamp()+interval '2 minutes' ELSE cleanup_after END
WHERE tenant_id=$1 AND owner_id=$2 AND id=$3 AND version=$4 AND state=$5 RETURNING `+columns, v.Owner.Tenant[:], v.Owner.Subject[:], v.ID[:], v.Version, v.State, to))
	if errors.Is(err, file.ErrNotFound) {
		return file.Record{}, file.ErrConflict
	}
	return result, err
}

// Claim fences previous workers by incrementing the row version. Worker deadlines
// must be shorter than this lease; stale workers cannot publish READY after takeover.
func (r *Repository) Claim(ctx context.Context) (file.Record, error) {
	return decode(r.rw.QueryRow(ctx, `UPDATE files.uploads SET lease_until=clock_timestamp()+interval '10 minutes',version=version+1
WHERE id=(SELECT id FROM files.uploads WHERE
(state IN ('SCANNING','REPLICATING') OR (state='UPLOADING' AND expires_at<=clock_timestamp()) OR (state IN ('READY','DELETED','EXPIRED','REJECTED') AND (cleanup_after IS NULL OR cleanup_after<=clock_timestamp())))
AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY updated_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+columns))
}

// MarkClean binds the output manifest before replication. A stale scanner loses CAS.
func (r *Repository) MarkClean(ctx context.Context, v file.Record, format file.Format, size int64, digest file.Digest) (file.Record, error) {
	if !validVersion(v) || v.State != file.Scanning || (format != file.PNG && format != file.JPEG && format != file.PDF) || size <= 0 || size > file.MaxSize || digest == (file.Digest{}) {
		return file.Record{}, file.ErrInvalid
	}
	result, err := decode(r.rw.QueryRow(ctx, `UPDATE files.uploads SET state='REPLICATING',clean_format=$5,clean_size=$6,clean_sha256=$7,version=version+1,updated_at=clock_timestamp(),lease_until=NULL
WHERE tenant_id=$1 AND owner_id=$2 AND id=$3 AND version=$4 AND state='SCANNING' RETURNING `+columns, v.Owner.Tenant[:], v.Owner.Subject[:], v.ID[:], v.Version, format, size, digest[:]))
	if errors.Is(err, file.ErrNotFound) {
		return file.Record{}, file.ErrConflict
	}
	return result, err
}

// DeferCleanup retains tombstones so previously issued upload URLs cannot revive data.
func (r *Repository) DeferCleanup(ctx context.Context, v file.Record) error {
	if !validVersion(v) || (v.State != file.Deleted && v.State != file.Rejected && v.State != file.Expired && v.State != file.Ready) {
		return file.ErrInvalid
	}
	command, err := r.rw.Exec(ctx, `UPDATE files.uploads SET cleanup_after=clock_timestamp()+interval '1 hour',lease_until=NULL,version=version+1 WHERE id=$1 AND tenant_id=$2 AND owner_id=$3 AND version=$4 AND state=$5`, v.ID[:], v.Owner.Tenant[:], v.Owner.Subject[:], v.Version, v.State)
	if err != nil {
		return file.ErrUnavailable
	}
	if command.RowsAffected() != 1 {
		return file.ErrConflict
	}
	return nil
}

func decode(row pgx.Row) (file.Record, error) {
	var r file.Record
	var id, tenant, owner, key, manifest, fingerprint, digest []byte
	err := row.Scan(&id, &tenant, &owner, &key, &r.ObjectKey, &manifest, &fingerprint, &r.UploadID, &r.State, &r.Version, &r.CreatedAt, &r.ExpiresAt, &r.CleanFormat, &r.CleanSize, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return file.Record{}, file.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "P0001" {
		return file.Record{}, file.ErrLimit
	}
	if err != nil {
		return file.Record{}, file.ErrUnavailable
	}
	for _, pair := range []struct {
		raw  []byte
		dest *file.ID
	}{{id, &r.ID}, {tenant, &r.Owner.Tenant}, {owner, &r.Owner.Subject}, {key, &r.IdempotencyKey}} {
		parsed, err := file.ParseID(pair.raw)
		if err != nil {
			return file.Record{}, file.ErrUnavailable
		}
		*pair.dest = parsed
	}
	if len(manifest) > 8192 || json.Unmarshal(manifest, &r.Manifest) != nil || r.Manifest.Validate() != nil || r.Version <= 0 || !r.ExpiresAt.After(r.CreatedAt) {
		return file.Record{}, file.ErrUnavailable
	}
	sum := r.Manifest.Fingerprint()
	object, objectErr := hex.DecodeString(r.ObjectKey)
	if !bytes.Equal(fingerprint, sum[:]) || objectErr != nil || len(object) != 16 || r.ObjectKey != hex.EncodeToString(object) || (r.UploadID != "" && r.UploadID != r.ID.String()) {
		return file.Record{}, file.ErrUnavailable
	}
	if len(digest) != 0 && len(digest) != 32 {
		return file.Record{}, file.ErrUnavailable
	}
	copy(r.CleanSHA256[:], digest)
	if r.State == file.Ready && (r.CleanSHA256 == (file.Digest{}) || r.CleanSize <= 0 || r.CleanSize > file.MaxSize || (r.CleanFormat != file.PNG && r.CleanFormat != file.JPEG && r.CleanFormat != file.PDF)) {
		return file.Record{}, file.ErrUnavailable
	}
	return r, nil
}
func validVersion(v file.Record) bool {
	return v.Owner.Valid() && v.ID != (file.ID{}) && v.Version > 0 && v.Version < math.MaxInt64
}
