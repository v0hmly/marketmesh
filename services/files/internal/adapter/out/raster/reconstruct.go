// Package raster reconstructs passive files from a deliberately small pixel protocol.
// No PDF, XML, ZIP or image decoder runs in the credentialed worker.
package raster

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

const (
	Magic     = "MMRGB1\n"
	MaxPages  = 100
	MaxPixels = 40_000_000
	MaxEdge   = 10_000
)

func Header(out io.Writer, pages uint32) error {
	if pages == 0 || pages > MaxPages {
		return file.ErrRejected
	}
	if _, err := io.WriteString(out, Magic); err != nil {
		return err
	}
	return binary.Write(out, binary.BigEndian, pages)
}

func Page(out io.Writer, img image.Image) error {
	b := img.Bounds()
	width, height := b.Dx(), b.Dy()
	if width <= 0 || height <= 0 || width > MaxEdge || height > MaxEdge || int64(width)*int64(height) > MaxPixels {
		return file.ErrRejected
	}
	if err := binary.Write(out, binary.BigEndian, [2]uint32{uint32(width), uint32(height)}); err != nil {
		return err
	}
	row := make([]byte, width*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, blue, a := img.At(x, y).RGBA()
			// Composite premultiplied channels onto white, preserving no metadata.
			i := (x - b.Min.X) * 3
			row[i], row[i+1], row[i+2] = byte((r+65535-a)>>8), byte((g+65535-a)>>8), byte((blue+65535-a)>>8)
		}
		if _, err := out.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// Reconstruct accepts only bounded RGB frames and writes a new PNG or image-only PDF.
func Reconstruct(input io.Reader, imageOutput bool, output io.Writer) (file.Format, error) {
	var magic [len(Magic)]byte
	if _, err := io.ReadFull(input, magic[:]); err != nil || string(magic[:]) != Magic {
		return "", file.ErrRejected
	}
	var pages uint32
	if err := binary.Read(input, binary.BigEndian, &pages); err != nil || pages == 0 || pages > MaxPages || (imageOutput && pages != 1) {
		return "", file.ErrRejected
	}
	var pdf *pdfWriter
	if !imageOutput {
		pdf = newPDF(output, int(pages))
	}
	var pixels uint64
	for i := uint32(0); i < pages; i++ {
		var dims [2]uint32
		if err := binary.Read(input, binary.BigEndian, &dims); err != nil || dims[0] == 0 || dims[1] == 0 || dims[0] > MaxEdge || dims[1] > MaxEdge {
			return "", file.ErrRejected
		}
		n := uint64(dims[0]) * uint64(dims[1])
		if n > MaxPixels-pixels {
			return "", file.ErrRejected
		}
		pixels += n
		rgb := make([]byte, int(n)*3)
		if _, err := io.ReadFull(input, rgb); err != nil {
			return "", file.ErrRejected
		}
		if imageOutput {
			img := image.NewNRGBA(image.Rect(0, 0, int(dims[0]), int(dims[1])))
			for j := 0; j < len(rgb)/3; j++ {
				copy(img.Pix[j*4:j*4+3], rgb[j*3:j*3+3])
				img.Pix[j*4+3] = 255
			}
			if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(output, img); err != nil {
				return "", err
			}
		} else if err := pdf.page(int(i), int(dims[0]), int(dims[1]), rgb); err != nil {
			return "", err
		}
	}
	var tail [1]byte
	if n, err := input.Read(tail[:]); n != 0 || err != io.EOF {
		return "", file.ErrRejected
	}
	if imageOutput {
		return file.PNG, nil
	}
	if err := pdf.finish(); err != nil {
		return "", err
	}
	return file.PDF, nil
}

type countingWriter struct {
	out   io.Writer
	count int64
	err   error
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.out.Write(p)
	w.count += int64(n)
	w.err = err
	if n != len(p) && err == nil {
		w.err = io.ErrShortWrite
	}
	return n, w.err
}

type pdfWriter struct {
	w       *countingWriter
	offsets []int64
}

func newPDF(out io.Writer, pages int) *pdfWriter {
	p := &pdfWriter{w: &countingWriter{out: out}, offsets: make([]int64, 3+pages*3)}
	fmt.Fprint(p.w, "%PDF-1.4\n%MarketMesh passive raster\n")
	p.object(1)
	fmt.Fprint(p.w, "<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	p.object(2)
	fmt.Fprintf(p.w, "<< /Type /Pages /Count %d /Kids [", pages)
	for i := 0; i < pages; i++ {
		fmt.Fprintf(p.w, "%d 0 R ", 3+i*3)
	}
	fmt.Fprint(p.w, "] >>\nendobj\n")
	return p
}
func (p *pdfWriter) object(id int) { p.offsets[id] = p.w.count; fmt.Fprintf(p.w, "%d 0 obj\n", id) }
func (p *pdfWriter) page(index, width, height int, rgb []byte) error {
	id := 3 + index*3
	p.object(id)
	fmt.Fprintf(p.w, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>\nendobj\n", width, height, id+1, id+2)
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	if _, err := z.Write(rgb); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	p.object(id + 1)
	fmt.Fprintf(p.w, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, height, compressed.Len())
	p.w.Write(compressed.Bytes())
	fmt.Fprint(p.w, "\nendstream\nendobj\n")
	commands := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", width, height)
	p.object(id + 2)
	fmt.Fprintf(p.w, "<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(commands), commands)
	return p.w.err
}
func (p *pdfWriter) finish() error {
	start := p.w.count
	fmt.Fprintf(p.w, "xref\n0 %d\n0000000000 65535 f \n", len(p.offsets))
	for _, offset := range p.offsets[1:] {
		fmt.Fprintf(p.w, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(p.w, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(p.offsets), start)
	return p.w.err
}
