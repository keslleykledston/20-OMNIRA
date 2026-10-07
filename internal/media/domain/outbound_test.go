package domain

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func outPNG(t *testing.T) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func outJPEG(t *testing.T) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestClassifyOutboundAppliesTheSafeAllowListAndTheChannel(t *testing.T) {
	img, doc := outPNG(t), []byte("%PDF-1.4\n1 0 obj<<>>endobj\n%%EOF")
	for _, tc := range []struct {
		name, provider string
		data           []byte
		declared       string
		reason         string // "" = accepted
	}{
		{"png on waha", "waha", img, "image/png", ""},
		{"png on meta", "meta_cloud", img, "", ""},
		{"pdf on meta", "meta_cloud", doc, "application/pdf", ""},
		{"zip is an archive", "waha", append([]byte("PK\x03\x04"), make([]byte, 40)...), "", ReasonArchive},
		{"exe", "waha", append([]byte("MZ"), make([]byte, 40)...), "", ReasonExecutable},
		{"html", "waha", []byte("<html><script>x</script></html>"), "text/plain", ReasonActiveContent},
		{"pdf with javascript", "waha", []byte("%PDF-1.4\n/JavaScript (app.alert(1))"), "", ReasonPDFActiveContent},
		{"an image that lies about being audio", "waha", img, "audio/ogg", ReasonDeclaredMismatch},
		{"unknown provider", "email", img, "", ReasonUnsupportedForChannel},
		{"empty", "waha", nil, "", ReasonEmpty},
		{"over the outbound ceiling", "waha", append([]byte("%PDF-"), make([]byte, MaxOutboundBytes)...), "", ReasonTooLarge},
	} {
		_, err := ClassifyOutbound(tc.data, tc.declared, tc.provider)
		reason, rejected := IsRejection(err)
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s: unexpected %v", tc.name, err)
		case tc.reason != "" && (!rejected || reason != tc.reason):
			t.Errorf("%s: got %v, want %s", tc.name, err, tc.reason)
		}
	}
	// WebP is a sticker on Meta: WAHA only.
	webp := append(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"), make([]byte, 6)...), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(webp[16:20], 10)
	if _, err := ClassifyOutbound(webp, "", "meta_cloud"); err == nil {
		t.Error("webp image must not go out through Meta")
	}
}

func TestSafeFileNameNeverCarriesPathsOrTheWrongExtension(t *testing.T) {
	for in, want := range map[string]string{
		"boleto março.pdf":       "boleto março.pdf",
		`..\..\windows\evil.exe`: "evil.pdf",
		"../../etc/passwd":       "passwd.pdf",
		"fatura‮gpj.exe":         "faturagpj.pdf",
		"":                       "arquivo.pdf",
		"   ...   ":              "arquivo.pdf",
		"a\x00b\nc.pdf":          "abc.pdf",
		strings.Repeat("x", 300): strings.Repeat("x", 100) + ".pdf",
		"report (final) v2.docx": "report (final) v2.pdf",
	} {
		if got := SafeFileName(in, "application/pdf"); got != want {
			t.Errorf("SafeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptionRules(t *testing.T) {
	if !CaptionValid("Segue o boleto 😀\nobrigado") || !CaptionValid("") {
		t.Fatal("plain captions must be valid")
	}
	if CaptionValid(strings.Repeat("a", MaxCaptionRunes+1)) || CaptionValid("x\x00y") || CaptionValid("‮txt") {
		t.Fatal("over-long captions, NUL and bidi overrides must be refused")
	}
}

func TestStripMetadataRemovesExifWithoutTouchingThePixels(t *testing.T) {
	clean := outJPEG(t)
	// Inject an APP1/EXIF segment carrying a GPS-looking payload right after SOI, and a comment.
	exif := append([]byte("Exif\x00\x00GPSLatitude=-23.55"), make([]byte, 8)...)
	seg := func(marker byte, payload []byte) []byte {
		l := make([]byte, 2)
		binary.BigEndian.PutUint16(l, uint16(len(payload)+2))
		return append(append([]byte{0xff, marker}, l...), payload...)
	}
	dirty := append(append(append([]byte{0xff, 0xd8}, seg(0xe1, exif)...), seg(0xfe, []byte("secret comment"))...), clean[2:]...)
	got, err := StripMetadata(dirty, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("GPSLatitude")) || bytes.Contains(got, []byte("secret comment")) {
		t.Fatal("metadata survived")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("the stripped JPEG no longer decodes: %v", err)
	}
	// PNG: add tEXt and eXIf chunks before IEND.
	p := outPNG(t)
	chunk := func(kind string, payload []byte) []byte {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(payload)))
		return append(append(append(l, kind...), payload...), 0, 0, 0, 0)
	}
	iend := len(p) - 12
	dirtyPNG := append(append(append(append([]byte{}, p[:iend]...), chunk("tEXt", []byte("Author\x00Maria"))...), chunk("eXIf", []byte("GPS"))...), p[iend:]...)
	gotPNG, err := StripMetadata(dirtyPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(gotPNG, []byte("Maria")) || bytes.Contains(gotPNG, []byte("eXIf")) {
		t.Fatal("PNG metadata survived")
	}
	if _, err := png.Decode(bytes.NewReader(gotPNG)); err != nil {
		t.Fatalf("the stripped PNG no longer decodes: %v", err)
	}
	// Other types are untouched; a truncated image is refused, not half-sent.
	if out, _ := StripMetadata([]byte("%PDF-"), "application/pdf"); string(out) != "%PDF-" {
		t.Fatal("non-images must pass through")
	}
	if _, err := StripMetadata(clean[:20], "image/jpeg"); err == nil {
		t.Fatal("a truncated JPEG must be refused")
	}
}
