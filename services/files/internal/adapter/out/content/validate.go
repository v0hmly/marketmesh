// Package content validates untrusted file containers before isolated AV/CDR.
package content

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

const (
	maxPixels   = 40_000_000
	maxEntries  = 4096
	maxExpanded = 512 * 1024 * 1024
	maxRatio    = 100
	maxXML      = 8 * 1024 * 1024
)

// Validate never trusts a filename or Content-Type without inspecting bytes.
// Decoding and conversion still run in a separate resource-limited sandbox.
func Validate(input io.ReaderAt, size int64, format file.Format) error {
	if input == nil || size <= 0 || size > file.MaxSize || format.Extension() == "" {
		return file.ErrRejected
	}
	reader := io.NewSectionReader(input, 0, size)
	switch format {
	case file.JPEG, file.PNG:
		cfg, actual, err := image.DecodeConfig(reader)
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > maxPixels/int64(cfg.Height) {
			return file.ErrRejected
		}
		if (format == file.PNG && actual != "png") || (format == file.JPEG && actual != "jpeg") {
			return file.ErrRejected
		}
		return nil
	case file.PDF:
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil || !bytes.HasPrefix(header[:], []byte("%PDF-")) {
			return file.ErrRejected
		}
		tail := make([]byte, min(size, 1024))
		if _, err := input.ReadAt(tail, size-int64(len(tail))); err != nil || !bytes.HasSuffix(bytes.TrimSpace(tail), []byte("%%EOF")) {
			return file.ErrRejected
		}
		return nil // The isolated PDF parser performs the full structural check.
	default:
		return validateOffice(input, size, format)
	}
}

func validateOffice(input io.ReaderAt, size int64, format file.Format) error {
	if !boundedDirectory(input, size) {
		return file.ErrRejected
	}
	z, err := zip.NewReader(input, size)
	if err != nil || len(z.File) == 0 || len(z.File) > maxEntries {
		return file.ErrRejected
	}
	seen := make(map[string]bool, len(z.File))
	var total uint64
	var odfType string
	var mainType string
	for _, entry := range z.File {
		name := entry.Name
		lower := strings.ToLower(name)
		if entry.FileInfo().IsDir() {
			if !safeName(strings.TrimSuffix(name, "/")) || entry.UncompressedSize64 != 0 || entry.Mode().Type() != fs.ModeDir {
				return file.ErrRejected
			}
			continue
		}
		if !safeName(name) || seen[lower] || entry.Mode().Type() != 0 || entry.Flags&1 != 0 {
			return file.ErrRejected
		}
		seen[lower] = true
		if entry.CompressedSize64 > uint64(size) || entry.UncompressedSize64 > maxExpanded-total || entry.UncompressedSize64 > max(1, entry.CompressedSize64)*maxRatio || forbiddenEntry(lower) {
			return file.ErrRejected
		}
		total += entry.UncompressedSize64
		if (strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") || strings.HasSuffix(lower, ".rdf")) && entry.UncompressedSize64 > maxXML {
			return file.ErrRejected
		}
		stream, err := entry.Open()
		if err != nil {
			return file.ErrRejected
		}
		err = inspectEntry(stream, entry, &odfType, &mainType)
		closeErr := stream.Close()
		if err != nil || closeErr != nil {
			return file.ErrRejected
		}
	}
	switch format {
	case file.DOCX:
		if !seen["word/document.xml"] || mainType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml" {
			return file.ErrRejected
		}
	case file.XLSX:
		if !seen["xl/workbook.xml"] || mainType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml" {
			return file.ErrRejected
		}
	case file.PPTX:
		if !seen["ppt/presentation.xml"] || mainType != "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml" {
			return file.ErrRejected
		}
	case file.ODT, file.ODS, file.ODP:
		if odfType != string(format) || !seen["content.xml"] || !seen["meta-inf/manifest.xml"] {
			return file.ErrRejected
		}
	default:
		return file.ErrRejected
	}
	return nil
}

func safeName(name string) bool {
	if len(name) == 0 || len(name) > 240 || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:%\x00\r\n") || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func forbiddenEntry(name string) bool {
	for _, part := range []string{"vbaproject", "macros", "scripts/", "basic/", "embeddings/", "oleobject", "externallinks/", "activex/", "encryption", "encryptedpackage"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	switch path.Ext(name) {
	case ".zip", ".7z", ".rar", ".gz", ".tar", ".doc", ".xls", ".ppt", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".pdf", ".exe", ".dll", ".js", ".vbs", ".bin":
		return true
	}
	return false
}

func inspectEntry(input io.Reader, entry *zip.File, odfType, mainType *string) error {
	// Read through EOF even for XML so ZIP CRC errors cannot be hidden by a parser.
	limit := int64(entry.UncompressedSize64)
	reader := io.LimitReader(input, limit+1)
	lower := strings.ToLower(entry.Name)
	if lower == "mimetype" || strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") || strings.HasSuffix(lower, ".rdf") {
		if limit > maxXML {
			return file.ErrRejected
		}
		data, err := io.ReadAll(reader)
		if err != nil || int64(len(data)) != limit {
			return file.ErrRejected
		}
		if lower == "mimetype" {
			*odfType = string(data)
			return nil
		}
		return inspectXML(data, lower, mainType)
	}
	var signature [8]byte
	nPrefix, err := io.ReadFull(reader, signature[:])
	if err != nil {
		return file.ErrRejected
	}
	imageType := ""
	switch {
	case bytes.Equal(signature[:], []byte{137, 80, 78, 71, 13, 10, 26, 10}):
		imageType = "png"
	case bytes.HasPrefix(signature[:], []byte{255, 216, 255}):
		imageType = "jpeg"
	default:
		return file.ErrRejected // Binary office parts are allowlisted by actual bytes.
	}
	ext := path.Ext(lower)
	if (imageType == "png" && ext != ".png") || (imageType == "jpeg" && ext != ".jpg" && ext != ".jpeg") {
		return file.ErrRejected
	}
	tail := &lastBytes{}
	tail.Write(signature[:])
	n, err := io.Copy(tail, reader)
	if err != nil || n+int64(nPrefix) != limit {
		return file.ErrRejected
	}
	if imageType == "png" && !bytes.Equal(tail.bytes[:], []byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 174, 66, 96, 130}) {
		return file.ErrRejected
	}
	if imageType == "jpeg" && !bytes.Equal(tail.bytes[10:], []byte{255, 217}) {
		return file.ErrRejected
	}
	return nil
}

type lastBytes struct{ bytes [12]byte }

func (t *lastBytes) Write(p []byte) (int, error) {
	n := len(p)
	if n >= len(t.bytes) {
		copy(t.bytes[:], p[n-len(t.bytes):])
	} else {
		copy(t.bytes[:], t.bytes[n:])
		copy(t.bytes[len(t.bytes)-n:], p)
	}
	return n, nil
}

// boundedDirectory checks the entry count before archive/zip allocates File objects.
// Files below 100 MiB never require ZIP64; multi-disk and prefixed archives are rejected.
func boundedDirectory(input io.ReaderAt, size int64) bool {
	if size < 22 {
		return false
	}
	tail := make([]byte, min(size, 65557))
	if _, err := input.ReadAt(tail, size-int64(len(tail))); err != nil {
		return false
	}
	// No ZIP comments: archive/zip and this preflight must select the same EOCD.
	end := len(tail) - 22
	if !bytes.Equal(tail[end:end+4], []byte{'P', 'K', 5, 6}) || binary.LittleEndian.Uint16(tail[end+20:]) != 0 {
		return false
	}
	h := tail[end:]
	entries := int(binary.LittleEndian.Uint16(h[10:12]))
	if binary.LittleEndian.Uint32(h[4:8]) != 0 || int(binary.LittleEndian.Uint16(h[8:10])) != entries || entries == 0 || entries > maxEntries {
		return false
	}
	length := int64(binary.LittleEndian.Uint32(h[12:16]))
	offset := int64(binary.LittleEndian.Uint32(h[16:20]))
	if length > 4*1024*1024 || offset+length != size-int64(len(tail))+int64(end) {
		return false
	}
	limit := offset + length
	for range entries {
		var entry [46]byte
		if offset+46 > limit {
			return false
		}
		if _, err := input.ReadAt(entry[:], offset); err != nil || !bytes.Equal(entry[:4], []byte{'P', 'K', 1, 2}) {
			return false
		}
		if binary.LittleEndian.Uint32(entry[20:24]) == 0xffffffff || binary.LittleEndian.Uint32(entry[24:28]) == 0xffffffff || binary.LittleEndian.Uint16(entry[34:36]) != 0 {
			return false
		}
		name := int64(binary.LittleEndian.Uint16(entry[28:30]))
		extra := int64(binary.LittleEndian.Uint16(entry[30:32]))
		comment := int64(binary.LittleEndian.Uint16(entry[32:34]))
		if name == 0 || name > 240 || extra > 4096 || comment > 4096 {
			return false
		}
		offset += 46 + name + extra + comment
	}
	return offset == limit
}

func inspectXML(data []byte, name string, mainType *string) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	roots := 0
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			if depth != 0 || roots != 1 {
				return file.ErrRejected
			}
			return nil
		}
		if err != nil {
			return file.ErrRejected
		}
		switch node := token.(type) {
		case xml.Directive:
			return file.ErrRejected // No DTD or entities from documents.
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 {
					return file.ErrRejected
				}
			}
			depth++
			if depth > 128 || len(node.Attr) > 128 {
				return file.ErrRejected
			}
			local := strings.ToLower(node.Name.Local)
			if local == "encryption-data" || local == "script" || local == "object" || local == "object-ole" {
				return file.ErrRejected
			}
			for _, attr := range node.Attr {
				if (strings.EqualFold(attr.Name.Local, "TargetMode") && !strings.EqualFold(attr.Value, "Internal")) || (strings.EqualFold(attr.Name.Local, "href") && (strings.Contains(attr.Value, ":") || strings.HasPrefix(attr.Value, "//"))) {
					return file.ErrRejected
				}
				if name == "[content_types].xml" && attr.Name.Local == "ContentType" {
					if strings.Contains(strings.ToLower(attr.Value), "macro") || strings.Contains(strings.ToLower(attr.Value), "activex") {
						return file.ErrRejected
					}
					if strings.HasSuffix(attr.Value, ".main+xml") {
						if *mainType != "" && *mainType != attr.Value {
							return file.ErrRejected
						}
						*mainType = attr.Value
					}
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(node)) != 0 {
				return file.ErrRejected
			}
		}
	}
}
