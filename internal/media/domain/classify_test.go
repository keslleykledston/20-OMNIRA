package domain

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.White)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func webpVP8X(w, h int) []byte {
	b := make([]byte, 30)
	copy(b[0:], "RIFF")
	copy(b[8:], "WEBPVP8X")
	b[24], b[25], b[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	b[27], b[28], b[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	binary.LittleEndian.PutUint32(b[4:], 22)
	return b
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	r, ok := IsRejection(err)
	if !ok {
		t.Fatalf("expected a Rejection, got %v", err)
	}
	return r
}

func TestClassifyAcceptsTheAllowList(t *testing.T) {
	ftyp := func(brand string) []byte {
		return append([]byte{0, 0, 0, 0x18}, []byte("ftyp"+brand+"\x00\x00\x00\x00isom")...)
	}
	cases := []struct {
		name string
		data []byte
		kind Kind
		mime string
	}{
		{"jpeg", jpegBytes(t), KindImage, "image/jpeg"},
		{"png", pngBytes(t, 4, 4), KindImage, "image/png"},
		{"gif", gifBytes(t), KindImage, "image/gif"},
		{"webp", webpVP8X(100, 100), KindImage, "image/webp"},
		{"whatsapp voice note", append([]byte("OggS\x00\x02"), make([]byte, 64)...), KindAudio, "audio/ogg"},
		{"mp3 with id3", append([]byte("ID3\x03\x00"), make([]byte, 64)...), KindAudio, "audio/mpeg"},
		{"mp3 raw frame", append([]byte{0xff, 0xfb, 0x90, 0x00}, make([]byte, 64)...), KindAudio, "audio/mpeg"},
		{"wav", append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...), KindAudio, "audio/wav"},
		{"m4a", ftyp("M4A "), KindAudio, "audio/mp4"},
		{"mp4", ftyp("isom"), KindVideo, "video/mp4"},
		{"webm", append([]byte{0x1a, 0x45, 0xdf, 0xa3}, make([]byte, 32)...), KindVideo, "video/webm"},
		{"clean pdf", []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n%%EOF"), KindDocument, "application/pdf"},
		{"plain text", []byte("nome;valor\nfulano;10\n"), KindDocument, "text/plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Classify(c.data, "")
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.Kind != c.kind || got.Mime != c.mime {
				t.Fatalf("got %+v, want %s %s", got, c.kind, c.mime)
			}
		})
	}
}

func TestClassifyRefusesHostileAndUnknownContent(t *testing.T) {
	cases := []struct {
		name   string
		data   []byte
		reason string
	}{
		{"empty", nil, ReasonEmpty},
		{"zip", append([]byte("PK\x03\x04"), make([]byte, 40)...), ReasonArchive},
		{"docx is a zip", append([]byte("PK\x03\x04"), []byte("word/document.xml")...), ReasonArchive},
		{"legacy office ole2", append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, make([]byte, 40)...), ReasonArchive},
		{"rar", append([]byte("Rar!\x1a\x07\x00"), make([]byte, 40)...), ReasonArchive},
		{"7z", append([]byte("7z\xbc\xaf\x27\x1c"), make([]byte, 40)...), ReasonArchive},
		{"gzip", append([]byte{0x1f, 0x8b, 8}, make([]byte, 40)...), ReasonArchive},
		{"windows exe", append([]byte("MZ\x90\x00"), make([]byte, 40)...), ReasonExecutable},
		{"elf", append([]byte("\x7fELF"), make([]byte, 40)...), ReasonExecutable},
		{"shell script", []byte("#!/bin/sh\nrm -rf /\n"), ReasonExecutable},
		{"wasm", append([]byte("\x00asm"), make([]byte, 40)...), ReasonExecutable},
		{"html", []byte("<!DOCTYPE html><html><script>alert(1)</script>"), ReasonActiveContent},
		{"html with bom and spaces", append([]byte{0xEF, 0xBB, 0xBF, ' ', '\n'}, []byte("<HTML>")...), ReasonActiveContent},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ReasonActiveContent},
		{"xml", []byte(`<?xml version="1.0"?><x/>`), ReasonActiveContent},
		{"php", []byte("<?php system($_GET['c']); ?>"), ReasonActiveContent},
		{"random binary", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}, ReasonTypeNotAllowed},
		{"text with nul", []byte("abc\x00def"), ReasonTypeNotAllowed},
		{"pdf with javascript", []byte("%PDF-1.7\n<< /OpenAction << /S /JavaScript /JS (app.alert(1)) >> >>"), ReasonPDFActiveContent},
		{"pdf with launch", []byte("%PDF-1.7\n<< /S /Launch /F (cmd.exe) >>"), ReasonPDFActiveContent},
		{"pdf with embedded file", []byte("%PDF-1.7\n<< /Type /EmbeddedFile >>"), ReasonPDFActiveContent},
		{"pdf with obfuscated name", []byte("%PDF-1.7\n<< /J#61vaScript (x) >>"), ReasonPDFActiveContent},
		{"jpeg with a php web shell appended", append(jpegBytes(t), []byte("<?php system($_GET['c']); ?>")...), ReasonPolyglot},
		{"png with a script appended", append(pngBytes(t, 4, 4), []byte("<script>alert(1)</script>")...), ReasonPolyglot},
		{"png pixel bomb", pngBytes(t, 4000, 4000), ReasonImageTooLarge},
		{"webp pixel bomb", webpVP8X(10000, 10000), ReasonImageTooLarge},
		{"truncated png header", []byte("\x89PNG\r\n\x1a\n\x00\x00"), ReasonImageUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Classify(c.data, "")
			if got := reasonOf(t, err); got != c.reason {
				t.Fatalf("reason = %q, want %q", got, c.reason)
			}
		})
	}
}

func TestClassifyEnforcesTheSizeLimit(t *testing.T) {
	big := append([]byte("OggS"), make([]byte, MaxBytes)...)
	if r := reasonOf(t, func() error { _, err := Classify(big, ""); return err }()); r != ReasonTooLarge {
		t.Fatalf("reason = %q", r)
	}
}

func TestClassifyDoesNotTrustTheDeclaredType(t *testing.T) {
	pdf := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF")
	// A PDF that calls itself an image is refused...
	_, err := Classify(pdf, "image/jpeg")
	if reasonOf(t, err) != ReasonDeclaredMismatch {
		t.Fatal("a PDF declared as image/jpeg must be refused")
	}
	// ...and the real bytes win even when the declaration is generic or missing.
	for _, declared := range []string{"", "application/octet-stream", "application/pdf; name=x"} {
		got, err := Classify(pdf, declared)
		if err != nil || got.Mime != "application/pdf" {
			t.Fatalf("declared %q: %+v %v", declared, got, err)
		}
	}
	// A voice note labelled video/mp4 by a client is still accepted as audio.
	ftypM4A := append([]byte{0, 0, 0, 0x18}, []byte("ftypM4A \x00\x00\x00\x00isom")...)
	if got, err := Classify(ftypM4A, "video/mp4"); err != nil || got.Kind != KindAudio {
		t.Fatalf("m4a declared as video/mp4: %+v %v", got, err)
	}
}

// The text file below is the standard EICAR antivirus test string. It is harmless plain text, so the type
// filter accepts it on purpose: catching it is the antivirus's job, which proves the two layers are independent.
func TestEICARPassesTheTypeFilterSoOnlyTheAntivirusStopsIt(t *testing.T) {
	eicar := `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
	got, err := Classify([]byte(strings.TrimSpace(eicar)), "text/plain")
	if err != nil || got.Mime != "text/plain" {
		t.Fatalf("EICAR as text: %+v %v", got, err)
	}
}
