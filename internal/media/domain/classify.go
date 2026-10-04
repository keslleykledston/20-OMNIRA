// Package domain holds the pure rules for inbound media (ADR-0016): what a file really is, whether it is
// allowed at all, and the limits it must respect. Nothing here touches the network, disk or database.
package domain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"unicode/utf8"
)

// Kind is derived from the real bytes, never from the declared mime type or file name.
type Kind string

const (
	KindImage    Kind = "image"
	KindAudio    Kind = "audio"
	KindVideo    Kind = "video"
	KindDocument Kind = "document"
)

// Limits approved by the owner (ADR-0016, decision 7). Duration limits (audio 10 min, video 2 min) are
// enforced where the media is decoded (M2 and M7), not here.
const (
	MaxBytes       = 25 << 20
	MaxImagePixels = 12_000_000
	maxTextBytes   = 1 << 20
	headerWindow   = 4096
)

// Reasons are stable identifiers stored in message_media.reason and shown to operators.
const (
	ReasonEmpty            = "empty"
	ReasonTooLarge         = "too_large"
	ReasonTypeNotAllowed   = "type_not_allowed"
	ReasonArchive          = "archive_not_allowed"
	ReasonExecutable       = "executable_not_allowed"
	ReasonActiveContent    = "active_content"
	ReasonPolyglot         = "polyglot"
	ReasonImageTooLarge    = "image_too_large"
	ReasonImageUnreadable  = "image_unreadable"
	ReasonPDFActiveContent = "pdf_active_content"
	ReasonDeclaredMismatch = "declared_type_mismatch"
)

// Rejection is a deliberate refusal: the file is never stored, scanned further, served or sent to a model.
type Rejection struct{ Reason string }

func (r *Rejection) Error() string { return "media rejected: " + r.Reason }

// IsRejection reports whether err is a Rejection and returns its reason.
func IsRejection(err error) (string, bool) {
	var rej *Rejection
	if errors.As(err, &rej) {
		return rej.Reason, true
	}
	return "", false
}

func reject(reason string) error { return &Rejection{Reason: reason} }

// Result is what the bytes really are.
type Result struct {
	Kind Kind
	Mime string
}

// Classify decides what data is by its content and refuses everything outside the allow-list. declaredMime
// is only used to catch a lie (an "image" that is really something else); it never grants a type.
func Classify(data []byte, declaredMime string) (Result, error) {
	if len(data) == 0 {
		return Result{}, reject(ReasonEmpty)
	}
	if len(data) > MaxBytes {
		return Result{}, reject(ReasonTooLarge)
	}

	// Things that must never be accepted, whatever they claim to be.
	if reason := forbiddenSignature(data); reason != "" {
		return Result{}, reject(reason)
	}

	res, ok := sniff(data)
	if !ok {
		return Result{}, reject(ReasonTypeNotAllowed)
	}

	if mismatch(declaredMime, res.Kind) {
		return Result{}, reject(ReasonDeclaredMismatch)
	}

	switch res.Kind {
	case KindImage:
		if containsActiveMarkup(data) {
			return Result{}, reject(ReasonPolyglot)
		}
		if err := checkImageDimensions(data, res.Mime); err != nil {
			return Result{}, err
		}
	case KindDocument:
		if res.Mime == "application/pdf" {
			if hasPDFActiveContent(data) {
				return Result{}, reject(ReasonPDFActiveContent)
			}
		}
	}
	return res, nil
}

// forbiddenSignature recognises containers and executables. Office documents (OOXML are ZIPs; legacy ones are
// OLE2) land here on purpose: v1 refuses them (ADR-0016, decision 6).
func forbiddenSignature(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")), bytes.HasPrefix(data, []byte("PK\x07\x08")),
		bytes.HasPrefix(data, []byte("Rar!\x1a\x07")), bytes.HasPrefix(data, []byte("7z\xbc\xaf\x27\x1c")),
		bytes.HasPrefix(data, []byte{0x1f, 0x8b}), bytes.HasPrefix(data, []byte("BZh")), bytes.HasPrefix(data, []byte{0xfd, '7', 'z', 'X', 'Z', 0}),
		bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}):
		return ReasonArchive
	case bytes.HasPrefix(data, []byte("MZ")), bytes.HasPrefix(data, []byte("\x7fELF")), bytes.HasPrefix(data, []byte("#!")),
		bytes.HasPrefix(data, []byte{0xfe, 0xed, 0xfa, 0xce}), bytes.HasPrefix(data, []byte{0xfe, 0xed, 0xfa, 0xcf}),
		bytes.HasPrefix(data, []byte{0xce, 0xfa, 0xed, 0xfe}), bytes.HasPrefix(data, []byte{0xcf, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(data, []byte{0xca, 0xfe, 0xba, 0xbe}), bytes.HasPrefix(data, []byte("\x00asm")):
		return ReasonExecutable
	}
	head := data
	if len(head) > headerWindow {
		head = head[:headerWindow]
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n")
	lower := bytes.ToLower(trimmed)
	for _, prefix := range []string{"<!doctype", "<html", "<svg", "<?xml", "<script", "<?php", "<body", "<iframe", "<head"} {
		if bytes.HasPrefix(lower, []byte(prefix)) {
			return ReasonActiveContent
		}
	}
	return ""
}

func sniff(data []byte) (Result, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return Result{KindImage, "image/jpeg"}, true
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return Result{KindImage, "image/png"}, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return Result{KindImage, "image/gif"}, true
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return Result{KindImage, "image/webp"}, true
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return Result{KindAudio, "audio/wav"}, true
	case bytes.HasPrefix(data, []byte("OggS")):
		return Result{KindAudio, "audio/ogg"}, true
	case bytes.HasPrefix(data, []byte("ID3")), isMPEGAudioFrame(data):
		return Result{KindAudio, "audio/mpeg"}, true
	case bytes.HasPrefix(data, []byte("#!AMR")):
		return Result{KindAudio, "audio/amr"}, true
	case bytes.HasPrefix(data, []byte("fLaC")):
		return Result{KindAudio, "audio/flac"}, true
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		return classifyFtyp(data[8:12]), true
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return Result{KindVideo, "video/webm"}, true
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return Result{KindDocument, "application/pdf"}, true
	case isPlainText(data):
		return Result{KindDocument, "text/plain"}, true
	}
	return Result{}, false
}

// classifyFtyp separates ISO-BMFF audio (M4A/AAC voice notes) from video by the major brand.
func classifyFtyp(brand []byte) Result {
	switch string(brand) {
	case "M4A ", "M4B ", "M4P ":
		return Result{KindAudio, "audio/mp4"}
	case "qt  ":
		return Result{KindVideo, "video/quicktime"}
	}
	return Result{KindVideo, "video/mp4"}
}

func isMPEGAudioFrame(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xff && data[1]&0xe0 == 0xe0 && data[1]&0x18 != 0x08 && data[1]&0x06 != 0
}

// isPlainText accepts only small, valid UTF-8 without control bytes. Anything that starts like markup or a
// script was already refused by forbiddenSignature.
func isPlainText(data []byte) bool {
	if len(data) > maxTextBytes || !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0 || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') {
			return false
		}
	}
	return true
}

// mismatch catches a declared type that contradicts the real one, e.g. "image/jpeg" that is a PDF. An
// empty or generic declaration is not a mismatch (WhatsApp often sends application/octet-stream).
func mismatch(declared string, real Kind) bool {
	d := declared
	for i := 0; i < len(d); i++ {
		if d[i] == ';' {
			d = d[:i]
			break
		}
	}
	var declaredKind Kind
	switch {
	case len(d) > 6 && d[:6] == "image/":
		declaredKind = KindImage
	case len(d) > 6 && d[:6] == "audio/":
		declaredKind = KindAudio
	case len(d) > 6 && d[:6] == "video/":
		declaredKind = KindVideo
	default:
		return false
	}
	// A WhatsApp voice note is audio/ogg; some clients label an MP4 audio track as video/mp4.
	if declaredKind == KindVideo && real == KindAudio {
		return false
	}
	return declaredKind != real
}

var activeMarkup = [][]byte{[]byte("<script"), []byte("<?php"), []byte("<%@"), []byte("<iframe"), []byte("javascript:")}

// containsActiveMarkup flags raster images that also carry script/web-shell markup (a classic polyglot).
func containsActiveMarkup(data []byte) bool {
	lower := bytes.ToLower(data)
	for _, m := range activeMarkup {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}

var pdfActive = [][]byte{[]byte("/JavaScript"), []byte("/JS"), []byte("/Launch"), []byte("/EmbeddedFile"), []byte("/RichMedia"), []byte("/SubmitForm")}

// hasPDFActiveContent refuses PDFs that can run code, launch programs or carry attachments. Name tokens can be
// obfuscated with #xx escapes, so any escaped name token in the dangerous family also counts.
func hasPDFActiveContent(data []byte) bool {
	for _, token := range pdfActive {
		if bytes.Contains(data, token) {
			return true
		}
	}
	return bytes.Contains(data, []byte("/J#")) || bytes.Contains(data, []byte("/Java#")) || bytes.Contains(data, []byte("/Launc#"))
}

// checkImageDimensions refuses decompression bombs by reading only the header.
func checkImageDimensions(data []byte, mime string) error {
	var w, h int
	switch mime {
	case "image/webp":
		var ok bool
		w, h, ok = webpDimensions(data)
		if !ok {
			return reject(ReasonImageUnreadable)
		}
	default:
		cfg, err := decodeConfig(data, mime)
		if err != nil {
			return reject(ReasonImageUnreadable)
		}
		w, h = cfg.Width, cfg.Height
	}
	if w <= 0 || h <= 0 {
		return reject(ReasonImageUnreadable)
	}
	if int64(w)*int64(h) > MaxImagePixels {
		return reject(ReasonImageTooLarge)
	}
	return nil
}

func decodeConfig(data []byte, mime string) (image.Config, error) {
	r := bytes.NewReader(data)
	switch mime {
	case "image/jpeg":
		return jpeg.DecodeConfig(r)
	case "image/png":
		return png.DecodeConfig(r)
	case "image/gif":
		return gif.DecodeConfig(r)
	}
	return image.Config{}, fmt.Errorf("unsupported image type %s", mime)
}

// webpDimensions reads the canvas size from the VP8 / VP8L / VP8X chunk header.
func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 30 {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff), true
	case "VP8L":
		if data[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8X":
		w := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
		h := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}
