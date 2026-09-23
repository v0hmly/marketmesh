package content

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func TestImageAndPDFTypes(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	if Validate(bytes.NewReader(pngData.Bytes()), int64(pngData.Len()), file.PNG) != nil {
		t.Fatal("valid PNG rejected")
	}
	for _, mime := range []file.Format{file.JPEG, file.PDF, file.DOCX, "image/svg+xml"} {
		if Validate(bytes.NewReader(pngData.Bytes()), int64(pngData.Len()), mime) == nil {
			t.Fatalf("type confused: %s", mime)
		}
	}
	pdf := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")
	if Validate(bytes.NewReader(pdf), int64(len(pdf)), file.PDF) != nil {
		t.Fatal("PDF envelope")
	}
	pdf = append(pdf, []byte("<script>trailing payload</script>")...)
	if Validate(bytes.NewReader(pdf), int64(len(pdf)), file.PDF) == nil {
		t.Fatal("trailing PDF payload")
	}
}

func office(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for name, body := range entries {
		f, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func docxEntries() map[string]string {
	return map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p>Hello</w:p></w:body></w:document>`,
	}
}

func TestOfficeStructureAndRejections(t *testing.T) {
	good := office(t, docxEntries())
	if Validate(bytes.NewReader(good), int64(len(good)), file.DOCX) != nil {
		t.Fatal("valid DOCX rejected")
	}
	if Validate(bytes.NewReader(good), int64(len(good)), file.XLSX) == nil {
		t.Fatal("DOCX disguised as XLSX")
	}
	for name, body := range map[string]string{
		"../escape.xml":                "<root/>",
		"/absolute.xml":                "<root/>",
		"word/vbaProject.bin":          "macro",
		"word/embeddings/object1.bin":  "ole",
		"word/nested.zip":              "archive",
		"word/media/disguised.png":     "PK\x03\x04renamed archive",
		"word/media/disguised.dat":     "PK\x03\x04renamed archive",
		"word/_rels/document.xml.rels": `<Relationships><Relationship TargetMode="External" Target="http://internal/secret"/></Relationships>`,
		"word/entity.xml":              `<!DOCTYPE root [<!ENTITY x SYSTEM "file:///etc/passwd">]><root>&x;</root>`,
		"word/deep.xml":                strings.Repeat("<a>", 129) + strings.Repeat("</a>", 129),
	} {
		t.Run(name, func(t *testing.T) {
			entries := docxEntries()
			entries[name] = body
			data := office(t, entries)
			if Validate(bytes.NewReader(data), int64(len(data)), file.DOCX) == nil {
				t.Fatal("unsafe office content accepted")
			}
		})
	}
	entries := docxEntries()
	entries["[Content_Types].xml"] = strings.ReplaceAll(entries["[Content_Types].xml"], "wordprocessingml.document.main+xml", "ms-word.document.macroEnabled.main+xml")
	data := office(t, entries)
	if Validate(bytes.NewReader(data), int64(len(data)), file.DOCX) == nil {
		t.Fatal("macro content type accepted")
	}
}

func TestArchiveBombAndCRC(t *testing.T) {
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for name, body := range docxEntries() {
		f, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	f, err := writer.Create("word/bomb.xml")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("<root>" + strings.Repeat(" ", 2*1024*1024) + "</root>"))
	writer.Close()
	if Validate(bytes.NewReader(out.Bytes()), int64(out.Len()), file.DOCX) == nil {
		t.Fatal("compression bomb accepted")
	}
	data := office(t, docxEntries())
	// Forge only the central-directory CRC. Every entry must be read through EOF.
	index := bytes.Index(data, []byte{'P', 'K', 1, 2})
	if index < 0 {
		t.Fatal("missing ZIP directory")
	}
	binary.LittleEndian.PutUint32(data[index+16:index+20], 0)
	if Validate(bytes.NewReader(data), int64(len(data)), file.DOCX) == nil {
		t.Fatal("invalid CRC accepted")
	}
}

func FuzzUntrustedContainer(f *testing.F) {
	f.Add([]byte("PK\x03\x04"), string(file.DOCX))
	f.Add([]byte("%PDF-1.7\n%%EOF"), string(file.PDF))
	f.Fuzz(func(t *testing.T, raw []byte, mime string) {
		if len(raw) > 2*1024*1024 {
			return
		}
		_ = Validate(bytes.NewReader(raw), int64(len(raw)), file.Format(mime))
	})
}

func TestZIPPreflightRejectsAmbiguousDirectory(t *testing.T) {
	original := office(t, docxEntries())
	for _, mutate := range []func([]byte) []byte{
		func(data []byte) []byte {
			end := len(data) - 22
			binary.LittleEndian.PutUint16(data[end+8:end+10], 4097)
			binary.LittleEndian.PutUint16(data[end+10:end+12], 4097)
			return data
		},
		func(data []byte) []byte {
			end := len(data) - 22
			binary.LittleEndian.PutUint16(data[end+20:], 22)
			return append(data, data[end:end+22]...)
		},
	} {
		data := mutate(bytes.Clone(original))
		if Validate(bytes.NewReader(data), int64(len(data)), file.DOCX) == nil {
			t.Fatal("ambiguous or unbounded directory accepted")
		}
	}
}
