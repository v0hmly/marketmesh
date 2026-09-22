package file

import (
	"crypto/sha256"
	"math"
	"testing"
)

func TestManifestBoundsAndFingerprint(t *testing.T) {
	digest := Digest(sha256.Sum256([]byte("document")))
	good := Manifest{Format: PDF, Size: PartSize + 1, SHA256: digest, Parts: []Part{{Size: PartSize, SHA256: digest}, {Size: 1, SHA256: digest}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, modify := range []func(*Manifest){
		func(m *Manifest) { m.Size = math.MaxInt64 },
		func(m *Manifest) { m.Size = 0 },
		func(m *Manifest) { m.Size++ },
		func(m *Manifest) { m.Format = "application/zip" },
		func(m *Manifest) { m.Format = "IMAGE/PNG" },
		func(m *Manifest) { m.Parts[0].Size-- },
		func(m *Manifest) { m.Parts[1].Size = math.MaxInt64 },
		func(m *Manifest) { m.Parts[1].SHA256 = Digest{} },
		func(m *Manifest) { m.Parts = nil },
	} {
		bad := good
		bad.Parts = append([]Part(nil), good.Parts...)
		modify(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted invalid manifest: %#v", bad)
		}
	}
	changed := good
	changed.Parts = append([]Part(nil), good.Parts...)
	changed.Parts[1].SHA256[0]++
	if good.Fingerprint() == changed.Fingerprint() {
		t.Fatal("part checksum not bound")
	}
	changed = good
	changed.Format = PNG
	if good.Fingerprint() == changed.Fingerprint() {
		t.Fatal("type not bound")
	}
}

func TestTerminalStatesCannotBecomeReady(t *testing.T) {
	for _, from := range []State{Uploading, Ready, Rejected, Expired, Deleted, "invalid"} {
		if CanTransition(from, Ready) {
			t.Fatalf("invalid READY transition from %s", from)
		}
	}
	if !CanTransition(Replicating, Ready) || !CanTransition(Ready, Deleted) || CanTransition(Deleted, Scanning) {
		t.Fatal("transition policy")
	}
}

func FuzzManifest(f *testing.F) {
	f.Add(int64(10), int64(10), "image/png", []byte("checksum"))
	f.Fuzz(func(t *testing.T, size, partSize int64, format string, raw []byte) {
		d := Digest(sha256.Sum256(raw))
		m := Manifest{Format: Format(format), Size: size, SHA256: d, Parts: []Part{{Size: partSize, SHA256: d}}}
		if m.Validate() == nil && (size <= 0 || size > MaxSize || partSize != size || partSize > PartSize || m.Format.Extension() == "") {
			t.Fatal("invalid acceptance")
		}
	})
}
