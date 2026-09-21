// Package s3store implements constrained S3 operations with separate principals.
package s3store

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type Credential struct{ AccessKey, SecretKey string }

type Config struct {
	APIEndpoint, PublicEndpoint string
	Bucket, KMSKey              string
	Control, Capability         Credential
	TLS                         *tls.Config
}

// Bucket binds one trust zone, KMS key and pair of IAM identities at startup.
type Bucket struct {
	control      *s3.Client
	presign      *s3.PresignClient
	name, kmsKey string
	publicURL    *url.URL
}

func NewBucket(cfg Config) (*Bucket, error) {
	api, err := endpoint(cfg.APIEndpoint)
	if err != nil {
		return nil, err
	}
	public, err := endpoint(cfg.PublicEndpoint)
	if err != nil {
		return nil, err
	}
	if cfg.Bucket == "" || len(cfg.Bucket) > 63 || strings.ContainsAny(cfg.Bucket, "/\\?%# ") || cfg.KMSKey == "" || cfg.Control.AccessKey == "" || cfg.Control.SecretKey == "" || ((cfg.Capability.AccessKey == "") != (cfg.Capability.SecretKey == "")) || cfg.Control.AccessKey == cfg.Capability.AccessKey || cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.RootCAs == nil {
		return nil, errors.New("s3store: invalid trust configuration")
	}
	tlsConfig := cfg.TLS.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.MaxConnsPerHost = 8
	transport.MaxIdleConnsPerHost = 4
	transport.ResponseHeaderTimeout = 10 * time.Second
	httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := func(base string, cred Credential) *s3.Client {
		return s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(base), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(cred.AccessKey, cred.SecretKey, ""), HTTPClient: httpClient, RetryMaxAttempts: 1})
	}
	bucket := &Bucket{control: client(api.String(), cfg.Control), name: cfg.Bucket, kmsKey: cfg.KMSKey, publicURL: public}
	if cfg.Capability.AccessKey != "" {
		bucket.presign = s3.NewPresignClient(client(public.String(), cfg.Capability))
	}
	return bucket, nil
}

func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("s3store: endpoint must be an explicit HTTPS origin")
	}
	u.Path = ""
	return u, nil
}

type Store struct {
	Quarantine *Bucket
	Internal   *Bucket
	Clean      []*Bucket
}

func New(quarantine, internal *Bucket, clean []*Bucket) (*Store, error) {
	if quarantine == nil || internal == nil || len(clean) != 2 || clean[0] == nil || clean[1] == nil || clean[0].publicURL.Host == clean[1].publicURL.Host {
		return nil, errors.New("s3store: two distinct clean DC endpoints required")
	}
	return &Store{Quarantine: quarantine, Internal: internal, Clean: append([]*Bucket(nil), clean...)}, nil
}

// NewControl has no access to the internal clean storage or parser worker roles.
func NewControl(quarantine *Bucket, clean []*Bucket) (*Store, error) {
	if quarantine == nil || quarantine.presign == nil || len(clean) != 2 || clean[0] == nil || clean[1] == nil || clean[0].presign == nil || clean[1].presign == nil || clean[0].publicURL.Host == clean[1].publicURL.Host {
		return nil, file.ErrInvalid
	}
	return &Store{Quarantine: quarantine, Clean: append([]*Bucket(nil), clean...)}, nil
}

// UploadID identifies the durable manifest. Parts are immutable S3 objects,
// because SeaweedFS ListParts omits SHA-256 even when it validated UploadPart.
func (s *Store) Begin(_ context.Context, r file.Record) (string, error) {
	if !validRecord(r) {
		return "", file.ErrInvalid
	}
	return r.ID.String(), nil
}

func (s *Store) Abort(ctx context.Context, r file.Record) error {
	if !validRecord(r) || r.UploadID != r.ID.String() {
		return file.ErrInvalid
	}
	var result error
	for i := range r.Manifest.Parts {
		_, err := s.Quarantine.control.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Quarantine.name), Key: aws.String(partKey(r, int32(i+1)))})
		result = errors.Join(result, safeError(ctx, err))
	}
	return result
}

func (s *Store) Parts(ctx context.Context, r file.Record) ([]files.ReceivedPart, error) {
	if !validRecord(r) || r.UploadID != r.ID.String() {
		return nil, file.ErrInvalid
	}
	parts := make([]files.ReceivedPart, 0, len(r.Manifest.Parts))
	for i := range r.Manifest.Parts {
		number := int32(i + 1)
		head, err := s.Quarantine.control.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Quarantine.name), Key: aws.String(partKey(r, number)), ChecksumMode: types.ChecksumModeEnabled})
		if errorCode(err) == "NotFound" || errorCode(err) == "NoSuchKey" {
			continue
		}
		if err != nil {
			return nil, safeError(ctx, err)
		}
		digest, err := base64.StdEncoding.Strict().DecodeString(aws.ToString(head.ChecksumSHA256))
		if err != nil || len(digest) != 32 || head.Metadata["file-id"] != r.ID.String() || head.Metadata["upload-id"] != r.UploadID || head.ServerSideEncryption != types.ServerSideEncryptionAwsKms || aws.ToString(head.SSEKMSKeyId) != s.Quarantine.kmsKey {
			return nil, file.ErrRejected
		}
		var sum file.Digest
		copy(sum[:], digest)
		parts = append(parts, files.ReceivedPart{Number: number, Size: aws.ToInt64(head.ContentLength), SHA256: sum, ETag: aws.ToString(head.ETag)})
	}
	return parts, nil
}

func (s *Store) SignPart(ctx context.Context, r file.Record, number int32, until time.Time) (files.Capability, error) {
	if s.Quarantine.presign == nil || !validRecord(r) || r.UploadID != r.ID.String() || number < 1 || int(number) > len(r.Manifest.Parts) {
		return files.Capability{}, file.ErrInvalid
	}
	ttl, err := duration(until)
	if err != nil {
		return files.Capability{}, err
	}
	part := r.Manifest.Parts[number-1]
	signed, err := s.Quarantine.presign.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Quarantine.name), Key: aws.String(partKey(r, number)), ContentLength: aws.Int64(part.Size), ContentType: aws.String("application/octet-stream"), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(part.SHA256[:])), ServerSideEncryption: types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String(s.Quarantine.kmsKey), Metadata: map[string]string{"file-id": r.ID.String(), "upload-id": r.UploadID}}, func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return files.Capability{}, safeError(ctx, err)
	}
	headers := make(map[string]string, len(signed.SignedHeader))
	for key, values := range signed.SignedHeader {
		if len(values) != 1 {
			return files.Capability{}, file.ErrUnavailable
		}
		if !strings.EqualFold(key, "Host") {
			headers[key] = values[0]
		}
	}
	return files.Capability{URL: signed.URL, Method: signed.Method, Headers: headers, ExpiresAt: until, PartNumber: number}, nil
}

// Complete validates the acknowledged immutable part set; the caller commits the
// logical completion once with its database CAS. No mutable S3 multipart handle remains.
func (s *Store) Complete(_ context.Context, r file.Record, parts []files.ReceivedPart) error {
	if !validRecord(r) || r.UploadID != r.ID.String() || len(parts) != len(r.Manifest.Parts) {
		return file.ErrInvalid
	}
	for i, p := range parts {
		if p.Number != int32(i+1) || p.Size != r.Manifest.Parts[i].Size || p.SHA256 != r.Manifest.Parts[i].SHA256 || p.ETag == "" {
			return file.ErrRejected
		}
	}
	return nil
}

func (s *Store) Completed(ctx context.Context, r file.Record) (bool, error) {
	parts, err := s.Parts(ctx, r)
	if err != nil {
		return false, err
	}
	if len(parts) != len(r.Manifest.Parts) {
		return false, nil
	}
	if err = s.Complete(ctx, r, parts); err != nil {
		return false, err
	}
	return true, nil
}

func partKey(r file.Record, number int32) string {
	return fmt.Sprintf("%s/parts/%04d", r.ObjectKey, number)
}

func (s *Store) SignDownload(ctx context.Context, r file.Record, until time.Time) (files.Capability, error) {
	if !validRecord(r) || r.State != file.Ready || r.CleanSize <= 0 || r.CleanSize > file.MaxSize || r.CleanSHA256 == (file.Digest{}) || r.CleanFormat.Extension() == "" {
		return files.Capability{}, file.ErrNotReady
	}
	for _, bucket := range s.Clean {
		if bucket.presign == nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		head, err := bucket.control.HeadObject(probeCtx, &s3.HeadObjectInput{Bucket: aws.String(bucket.name), Key: aws.String(r.ObjectKey)})
		cancel()
		if err != nil {
			continue
		}
		if aws.ToInt64(head.ContentLength) != r.CleanSize || head.Metadata["clean-sha256"] != hex.EncodeToString(r.CleanSHA256[:]) || head.ServerSideEncryption != types.ServerSideEncryptionAwsKms || aws.ToString(head.SSEKMSKeyId) != bucket.kmsKey {
			continue
		}
		ttl, err := duration(until)
		if err != nil {
			return files.Capability{}, err
		}
		signed, err := bucket.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.name), Key: aws.String(r.ObjectKey), ResponseContentType: aws.String(string(r.CleanFormat)), ResponseContentDisposition: aws.String("attachment; filename=\"" + r.ID.String() + "." + r.CleanFormat.Extension() + "\""), ResponseCacheControl: aws.String("private, no-store")}, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			continue
		}
		return files.Capability{URL: signed.URL, Method: http.MethodGet, ExpiresAt: until}, nil
	}
	return files.Capability{}, file.ErrUnavailable
}

func validRecord(r file.Record) bool {
	raw, err := hex.DecodeString(r.ObjectKey)
	return err == nil && len(raw) == 16 && r.ObjectKey == strings.ToLower(r.ObjectKey) && r.ID != (file.ID{}) && r.Owner.Valid() && r.Manifest.Validate() == nil
}
func duration(until time.Time) (time.Duration, error) {
	ttl := time.Until(until)
	if ttl < time.Second || ttl > file.CapabilityTTL {
		return 0, file.ErrInvalid
	}
	return ttl.Truncate(time.Second), nil
}
func errorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

// SDK errors can contain presigned URLs; never return them to logging or transport.
func safeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return file.ErrUnavailable
}

var _ files.Storage = (*Store)(nil)
