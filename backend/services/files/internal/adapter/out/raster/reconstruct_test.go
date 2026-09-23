package raster

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func frame(t *testing.T) []byte {
	t.Helper()
	var input bytes.Buffer
	if err := Header(&input, 1); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.Transparent)
	if err := Page(&input, img); err != nil {
		t.Fatal(err)
	}
	return input.Bytes()
}
func TestPassiveReconstruction(t *testing.T) {
	input := frame(t)
	var output bytes.Buffer
	format, err := Reconstruct(bytes.NewReader(input), true, &output)
	if err != nil || format != file.PNG {
		t.Fatalf("PNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != image.Rect(0, 0, 2, 1) {
		t.Fatal("dimensions changed")
	}
	r, g, b, _ := img.At(1, 0).RGBA()
	if r != 65535 || g != 65535 || b != 65535 {
		t.Fatal("transparent pixel not flattened")
	}
	output.Reset()
	format, err = Reconstruct(bytes.NewReader(input), false, &output)
	if err != nil || format != file.PDF || !bytes.HasPrefix(output.Bytes(), []byte("%PDF-1.4")) || !bytes.HasSuffix(output.Bytes(), []byte("%%EOF\n")) {
		t.Fatal("invalid passive PDF", err)
	}
	for _, active := range []string{"/JavaScript", "/OpenAction", "/EmbeddedFile", "/URI", "/Annots", "/AcroForm"} {
		if bytes.Contains(output.Bytes(), []byte(active)) {
			t.Fatal("active content")
		}
	}
}
func TestHostileFrames(t *testing.T) {
	valid := frame(t)
	cases := [][]byte{nil, valid[:len(valid)-1], append(bytes.Clone(valid), 0)}
	for _, dims := range [][2]uint32{{0, 1}, {1, 0}, {0xffffffff, 1}, {10000, 10000}} {
		var input bytes.Buffer
		Header(&input, 1)
		binary.Write(&input, binary.BigEndian, dims)
		cases = append(cases, input.Bytes())
	}
	var many bytes.Buffer
	many.WriteString(Magic)
	binary.Write(&many, binary.BigEndian, uint32(MaxPages+1))
	cases = append(cases, many.Bytes())
	for i, input := range cases {
		if _, err := Reconstruct(bytes.NewReader(input), false, io.Discard); err == nil {
			t.Fatalf("accepted case %d", i)
		}
	}
}
func FuzzFrameBounds(f *testing.F) {
	f.Add([]byte("MMRGB1\n\x00\x00\x00\x01\x00\x00\x00\x01\x00\x00\x00\x01rgb"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			t.Skip()
		}
		Reconstruct(bytes.NewReader(raw), false, io.Discard)
	})
}
