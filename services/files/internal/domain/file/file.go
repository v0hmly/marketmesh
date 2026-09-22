// Package file defines bounded upload manifests and durable file states.
package file

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

const (
	MaxSize       = 100 * 1024 * 1024
	PartSize      = 5 * 1024 * 1024
	MaxParts      = MaxSize / PartSize
	CapabilityTTL = time.Minute
	UploadTTL     = 24 * time.Hour
	ProcessingTTL = 48 * time.Hour
)

var (
	ErrInvalid     = errors.New("invalid file request")
	ErrNotFound    = errors.New("file not found")
	ErrConflict    = errors.New("file state conflict")
	ErrNotReady    = errors.New("file not ready")
	ErrUnavailable = errors.New("file dependency unavailable")
	ErrRejected    = errors.New("file rejected")
	ErrLimit       = errors.New("file quota reached")
)

// ID is opaque and never derived from a client filename or storage path.
type ID [16]byte

func ParseID(raw []byte) (ID, error) {
	var id ID
	if len(raw) != len(id) {
		return id, ErrInvalid
	}
	copy(id[:], raw)
	if id == (ID{}) {
		return id, ErrInvalid
	}
	return id, nil
}

func (id ID) String() string { return hex.EncodeToString(id[:]) }

// Owner is supplied only by the authenticated application context.
type Owner struct{ Tenant, Subject ID }

func (o Owner) Valid() bool { return o.Tenant != (ID{}) && o.Subject != (ID{}) }

type State string

const (
	Uploading   State = "UPLOADING"
	Scanning    State = "SCANNING"
	Replicating State = "REPLICATING"
	Ready       State = "READY"
	Rejected    State = "REJECTED"
	Expired     State = "EXPIRED"
	Deleted     State = "DELETED"
)

type Format string

const (
	JPEG Format = "image/jpeg"
	PNG  Format = "image/png"
	PDF  Format = "application/pdf"
	DOCX Format = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	XLSX Format = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	PPTX Format = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	ODT  Format = "application/vnd.oasis.opendocument.text"
	ODS  Format = "application/vnd.oasis.opendocument.spreadsheet"
	ODP  Format = "application/vnd.oasis.opendocument.presentation"
)

func (f Format) Extension() string {
	switch f {
	case JPEG:
		return "jpg"
	case PNG:
		return "png"
	case PDF:
		return "pdf"
	case DOCX:
		return "docx"
	case XLSX:
		return "xlsx"
	case PPTX:
		return "pptx"
	case ODT:
		return "odt"
	case ODS:
		return "ods"
	case ODP:
		return "odp"
	default:
		return ""
	}
}

type Digest [sha256.Size]byte

// Part fixes the accepted content before any upload capability is issued.
type Part struct {
	Size   int64
	SHA256 Digest
}

type Manifest struct {
	Format Format
	Size   int64
	SHA256 Digest
	Parts  []Part
}

func (m Manifest) Validate() error {
	if m.Format.Extension() == "" || m.Size <= 0 || m.Size > MaxSize || m.SHA256 == (Digest{}) || len(m.Parts) == 0 || len(m.Parts) > MaxParts {
		return ErrInvalid
	}
	var total int64
	for i, p := range m.Parts {
		if p.Size <= 0 || p.Size > PartSize || p.SHA256 == (Digest{}) || (i < len(m.Parts)-1 && p.Size != PartSize) {
			return ErrInvalid
		}
		total += p.Size // At most MaxParts bounded values, so addition cannot overflow.
	}
	if total != m.Size {
		return ErrInvalid
	}
	return nil
}

// Fingerprint is stable across retries and binds every content constraint.
func (m Manifest) Fingerprint() Digest {
	h := sha256.New()
	h.Write([]byte("marketmesh-file-manifest-v1\x00"))
	h.Write([]byte(m.Format))
	h.Write([]byte{0})
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(m.Size))
	h.Write(number[:])
	h.Write(m.SHA256[:])
	for _, p := range m.Parts {
		binary.BigEndian.PutUint64(number[:], uint64(p.Size))
		h.Write(number[:])
		h.Write(p.SHA256[:])
	}
	var digest Digest
	copy(digest[:], h.Sum(nil))
	return digest
}

// Record contains only Files metadata; storage credentials are never persisted here.
type Record struct {
	ID                   ID
	Owner                Owner
	IdempotencyKey       ID
	Manifest             Manifest
	ObjectKey            string
	UploadID             string
	State                State
	Version              int64
	CreatedAt, ExpiresAt time.Time
	CleanFormat          Format
	CleanSize            int64
	CleanSHA256          Digest
}

func CanTransition(from, to State) bool {
	if to == Deleted {
		switch from {
		case Uploading, Scanning, Replicating, Ready, Rejected, Expired:
			return true
		default:
			return false
		}
	}
	switch from {
	case Uploading:
		return to == Scanning || to == Expired || to == Rejected
	case Scanning:
		return to == Replicating || to == Rejected || to == Expired
	case Replicating:
		return to == Ready || to == Rejected || to == Expired
	default:
		return false
	}
}
