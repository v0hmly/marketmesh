package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

// ReadUpload rechecks actual bytes, independent of S3 metadata and ETags.
func (s *Store) ReadUpload(ctx context.Context, r file.Record, output io.Writer) error {
	if !validRecord(r) || r.UploadID != r.ID.String() || r.State != file.Scanning || output == nil {
		return file.ErrInvalid
	}
	whole := sha256.New()
	for i, expected := range r.Manifest.Parts {
		got, err := s.Quarantine.control.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Quarantine.name), Key: aws.String(partKey(r, int32(i+1))), ChecksumMode: types.ChecksumModeEnabled})
		if err != nil {
			return safeError(ctx, err)
		}
		part := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(output, whole, part), io.LimitReader(got.Body, expected.Size+1))
		closeErr := got.Body.Close()
		if copyErr != nil || closeErr != nil {
			return file.ErrUnavailable
		}
		if n != expected.Size || !bytes.Equal(part.Sum(nil), expected.SHA256[:]) || got.ServerSideEncryption != types.ServerSideEncryptionAwsKms || aws.ToString(got.SSEKMSKeyId) != s.Quarantine.kmsKey {
			return file.ErrRejected
		}
	}
	if !bytes.Equal(whole.Sum(nil), r.Manifest.SHA256[:]) {
		return file.ErrRejected
	}
	return nil
}

func cleanKey(r file.Record, sum file.Digest) string {
	return r.ObjectKey + "/clean/" + hex.EncodeToString(sum[:])
}

// PutClean uses a content-addressed candidate so competing or stale scanners
// cannot overwrite the candidate referenced by the winning database version.
func (s *Store) PutClean(ctx context.Context, r file.Record, format file.Format, input io.ReadSeeker, size int64) (file.Digest, error) {
	if s.Internal == nil || !validRecord(r) || r.State != file.Scanning || input == nil || size <= 0 || size > file.MaxSize || (format != file.PNG && format != file.PDF) {
		return file.Digest{}, file.ErrInvalid
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(input, size+1))
	if err != nil || n != size {
		return file.Digest{}, file.ErrRejected
	}
	var sum file.Digest
	copy(sum[:], h.Sum(nil))
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return file.Digest{}, file.ErrUnavailable
	}
	if err = put(ctx, s.Internal, cleanKey(r, sum), format, input, size, sum); err != nil {
		return file.Digest{}, err
	}
	if err = verify(ctx, s.Internal, cleanKey(r, sum), size, sum); err != nil {
		return file.Digest{}, err
	}
	return sum, nil
}

func put(ctx context.Context, b *Bucket, key string, format file.Format, input io.Reader, size int64, sum file.Digest) error {
	_, err := b.control.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.name), Key: aws.String(key), Body: input, ContentLength: aws.Int64(size), ContentType: aws.String(string(format)), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])), ServerSideEncryption: types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String(b.kmsKey), Metadata: map[string]string{"clean-sha256": hex.EncodeToString(sum[:])}})
	if errorCode(err) == "PreconditionFailed" {
		return nil
	} // Caller verifies existing bytes.
	return safeError(ctx, err)
}

func readClean(ctx context.Context, b *Bucket, key string, size int64, sum file.Digest, output io.Writer) error {
	got, err := b.control.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		return safeError(ctx, err)
	}
	defer got.Body.Close()
	if aws.ToInt64(got.ContentLength) != size || got.Metadata["clean-sha256"] != hex.EncodeToString(sum[:]) || got.ServerSideEncryption != types.ServerSideEncryptionAwsKms || aws.ToString(got.SSEKMSKeyId) != b.kmsKey {
		return file.ErrRejected
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, h), io.LimitReader(got.Body, size+1))
	if err != nil {
		return file.ErrUnavailable
	}
	if n != size || !bytes.Equal(h.Sum(nil), sum[:]) {
		return file.ErrRejected
	}
	return nil
}

func verify(ctx context.Context, b *Bucket, key string, size int64, sum file.Digest) error {
	return readClean(ctx, b, key, size, sum, io.Discard)
}

// Replicate acknowledges only after reading back both independently encrypted
// delivery copies. A partial attempt is retried with immutable conditional puts.
func (s *Store) Replicate(ctx context.Context, r file.Record) error {
	if s.Internal == nil || len(s.Clean) != 2 || !validRecord(r) || r.State != file.Replicating || r.CleanSize <= 0 || r.CleanSize > file.MaxSize || r.CleanSHA256 == (file.Digest{}) || (r.CleanFormat != file.PNG && r.CleanFormat != file.PDF) {
		return file.ErrInvalid
	}
	var data bytes.Buffer
	if err := readClean(ctx, s.Internal, cleanKey(r, r.CleanSHA256), r.CleanSize, r.CleanSHA256, &data); err != nil {
		return err
	}
	for _, dc := range s.Clean {
		if err := put(ctx, dc, r.ObjectKey, r.CleanFormat, bytes.NewReader(data.Bytes()), r.CleanSize, r.CleanSHA256); err != nil {
			return err
		}
		if err := verify(ctx, dc, r.ObjectKey, r.CleanSize, r.CleanSHA256); err != nil {
			return err
		}
	}
	return nil
}

// Purge retains the durable tombstone; repeated reconciliation removes late
// writes from already-issued capabilities and fenced workers as well.
func (s *Store) Purge(ctx context.Context, r file.Record) error {
	if !validRecord(r) || (r.State != file.Deleted && r.State != file.Rejected && r.State != file.Expired) {
		return file.ErrInvalid
	}
	var result error
	if r.UploadID != "" {
		result = s.Abort(ctx, r)
	}
	for _, b := range s.Clean {
		_, err := b.control.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.name), Key: aws.String(r.ObjectKey)})
		result = errors.Join(result, safeError(ctx, err))
	}
	return errors.Join(result, s.purgeCandidates(ctx, r, ""))
}

// CleanupReady removes the source and losing candidates, retaining only the
// immutable internal derivative referenced by the durable READY manifest.
func (s *Store) CleanupReady(ctx context.Context, r file.Record) error {
	if !validRecord(r) || r.State != file.Ready || r.CleanSHA256 == (file.Digest{}) {
		return file.ErrInvalid
	}
	return errors.Join(s.Abort(ctx, r), s.purgeCandidates(ctx, r, cleanKey(r, r.CleanSHA256)))
}

func (s *Store) purgeCandidates(ctx context.Context, r file.Record, keep string) error {
	var result error
	if s.Internal == nil {
		return errors.Join(result, file.ErrUnavailable)
	}
	prefix := r.ObjectKey + "/clean/"
	listed, err := s.Internal.control.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.Internal.name), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1000)})
	if err != nil {
		return errors.Join(result, safeError(ctx, err))
	}
	for _, obj := range listed.Contents {
		key := aws.ToString(obj.Key)
		if !strings.HasPrefix(key, prefix) || len(key) != len(prefix)+64 {
			return errors.Join(result, file.ErrRejected)
		}
		if key == keep {
			continue
		}
		_, err = s.Internal.control.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Internal.name), Key: aws.String(key)})
		result = errors.Join(result, safeError(ctx, err))
	}
	if aws.ToBool(listed.IsTruncated) {
		result = errors.Join(result, file.ErrUnavailable)
	}
	return result
}
