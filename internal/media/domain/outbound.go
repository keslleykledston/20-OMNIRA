package domain

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"image/png"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Outbound media (ADR-0024): a file an operator sends to a customer. The same content-based allow-list as inbound media applies (the
// operator's browser is not trusted to say what a file is, and a customer's phone should never receive an executable, an archive or a
// PDF that runs code), narrowed to what WhatsApp can actually deliver as media, plus metadata stripping for images.
const (
	MaxOutboundBytes = 16 << 20 // WhatsApp's media ceiling; Meta documents allow more, but one limit keeps the rule simple
	MaxCaptionRunes  = 1024
	maxFileNameRunes = 100

	// ReasonUnsupportedForChannel: the file is safe but the connection's provider cannot deliver it as media.
	ReasonUnsupportedForChannel = "unsupported_for_channel"
)

// Provider names as stored in channel_connections.provider.
const (
	providerWAHA = "waha"
	providerMeta = "meta_cloud"
)

// OutboundMediaSupported reports whether a provider can send media at all.
func OutboundMediaSupported(provider string) bool {
	return provider == providerWAHA || provider == providerMeta
}

// ClassifyOutbound decides what the bytes really are (same rules and limits as inbound) and whether the provider can deliver them.
func ClassifyOutbound(data []byte, declaredMime, provider string) (Result, error) {
	if len(data) > MaxOutboundBytes {
		return Result{}, reject(ReasonTooLarge)
	}
	res, err := Classify(data, declaredMime)
	if err != nil {
		return Result{}, err
	}
	if !OutboundMediaSupported(provider) || !channelAllows(provider, res) {
		return Result{}, reject(ReasonUnsupportedForChannel)
	}
	return res, nil
}

// ValidateStructure fully decodes what it can decode (JPEG, PNG: the whole image, so a truncated or malformed file is refused, not
// "mostly valid") and checks the length declared by a WebP container. The pixel ceiling was already enforced from the header.
func ValidateStructure(data []byte, mime string) error {
	switch mime {
	case "image/jpeg":
		if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
			return reject(ReasonImageUnreadable)
		}
	case "image/png":
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			return reject(ReasonImageUnreadable)
		}
	case "image/webp":
		if len(data) < 12 || int(binary.LittleEndian.Uint32(data[4:8]))+8 != len(data) {
			return reject(ReasonImageUnreadable) // trailing bytes or a truncated container
		}
	}
	return nil
}

// OutboundMimeAllowed re-checks a stored attachment's type against the provider of the conversation's CURRENT connection.
func OutboundMimeAllowed(provider, mime string) bool {
	return OutboundMediaSupported(provider) && channelAllows(provider, Result{Mime: mime})
}

// channelAllows is the intersection of the safe allow-list with what each provider accepts as media.
func channelAllows(provider string, r Result) bool {
	switch r.Mime {
	case "image/jpeg", "image/png", // both
		"audio/ogg", "audio/mpeg", "audio/mp4", "audio/amr", // voice notes and audio
		"video/mp4",
		"application/pdf", "text/plain":
		return true
	case "image/webp":
		return provider == providerWAHA // Meta takes WebP only as a sticker
	}
	return false
}

var mimeExtension = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/amr": ".amr",
	"video/mp4":       ".mp4",
	"application/pdf": ".pdf", "text/plain": ".txt",
}

// SafeFileName builds the name the customer sees: the operator's name reduced to letters, digits and a few separators (no path, no
// control or bidirectional characters, bounded length) with the extension of the REAL type, so "boleto.pdf.exe" cannot be sent as such.
func SafeFileName(declared, mime string) string {
	base := declared
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	var b strings.Builder
	n := 0
	for _, r := range base {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '(' || r == ')':
			b.WriteRune(r)
		default:
			continue
		}
		if n++; n >= maxFileNameRunes {
			break
		}
	}
	name := strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .-_")
	if name == "" {
		name = "arquivo"
	}
	return name + mimeExtension[mime]
}

// CaptionValid: WhatsApp captions are plain text up to 1024 characters.
func CaptionValid(caption string) bool {
	return utf8.RuneCountInString(caption) <= MaxCaptionRunes && !containsControl(caption)
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
		if r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return true
		}
	}
	return false
}

// StripMetadata removes the privacy-sensitive metadata of an image (EXIF with GPS and device, XMP, IPTC, text chunks) WITHOUT
// re-encoding the pixels. Other types are returned unchanged. A malformed image is refused rather than sent half-parsed.
func StripMetadata(data []byte, mime string) ([]byte, error) {
	switch mime {
	case "image/jpeg":
		return stripJPEG(data)
	case "image/png":
		return stripPNG(data)
	}
	return data, nil
}

func stripJPEG(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return nil, reject(ReasonImageUnreadable)
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xff, 0xd8)
	i := 2
	for i < len(data) {
		if data[i] != 0xff {
			return nil, reject(ReasonImageUnreadable)
		}
		for i < len(data) && data[i] == 0xff { // fill bytes
			i++
		}
		if i >= len(data) {
			return nil, reject(ReasonImageUnreadable)
		}
		marker := data[i]
		i++
		if marker == 0xd9 { // EOI
			out = append(out, 0xff, 0xd9)
			return out, nil
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) { // no payload
			out = append(out, 0xff, marker)
			continue
		}
		if i+2 > len(data) {
			return nil, reject(ReasonImageUnreadable)
		}
		length := int(binary.BigEndian.Uint16(data[i : i+2]))
		if length < 2 || i+length > len(data) {
			return nil, reject(ReasonImageUnreadable)
		}
		segment := data[i : i+length]
		if marker == 0xda { // SOS: the scans follow; keep them up to the first genuine EOI and nothing after it
			out = append(out, 0xff, marker)
			out = append(out, data[i:i+length]...)
			res, ok := copyScans(out, data, i+length)
			if !ok {
				return nil, reject(ReasonImageUnreadable) // truncated: no EOI
			}
			return res, nil
		}
		// drop APP1 (EXIF, XMP), APP13 (Photoshop/IPTC) and comments; keep JFIF (APP0), ICC colour profile (APP2) and the codec segments
		if marker != 0xe1 && marker != 0xed && marker != 0xfe {
			out = append(out, 0xff, marker)
			out = append(out, segment...)
		}
		i += length
	}
	return nil, reject(ReasonImageUnreadable)
}

// copyScans copies the scans that start at pos (progressive JPEGs have several, separated by DHT/SOS segments whose payloads may contain
// any byte) up to and including the genuine EOI, dropping metadata segments (APP1/APP13/COM) found between scans, and returns the
// extended output. In entropy data every 0xFF is followed by 0x00 (stuffing) or a restart marker, so any other 0xFF xx is a real marker.
func copyScans(out, data []byte, pos int) ([]byte, bool) {
	from := pos
	for pos+1 < len(data) {
		if data[pos] != 0xff {
			pos++
			continue
		}
		m := data[pos+1]
		switch {
		case m == 0x00 || (m >= 0xd0 && m <= 0xd7):
			pos += 2
		case m == 0xff:
			pos++
		case m == 0xd9:
			return append(out, data[from:pos+2]...), true
		default: // another segment (DHT, SOS, DNL, APPn, ...): skip its declared length
			if pos+4 > len(data) {
				return nil, false
			}
			l := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
			if l < 2 || pos+2+l > len(data) {
				return nil, false
			}
			if m == 0xe1 || m == 0xed || m == 0xfe { // metadata between scans: leave it out
				out = append(out, data[from:pos]...)
				from = pos + 2 + l
			}
			pos += 2 + l
		}
	}
	return nil, false
}

func stripPNG(data []byte) ([]byte, error) {
	sig := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(data, sig) {
		return nil, reject(ReasonImageUnreadable)
	}
	out := append(make([]byte, 0, len(data)), sig...)
	i := len(sig)
	first := true
	for i < len(data) {
		if i+8 > len(data) {
			return nil, reject(ReasonImageUnreadable)
		}
		length := int(binary.BigEndian.Uint32(data[i : i+4]))
		end := i + 8 + length + 4
		if length < 0 || end > len(data) || end < i {
			return nil, reject(ReasonImageUnreadable)
		}
		kind := string(data[i+4 : i+8])
		if first && kind != "IHDR" {
			return nil, reject(ReasonImageUnreadable)
		}
		first = false
		switch kind {
		case "eXIf", "tEXt", "zTXt", "iTXt", "tIME": // metadata chunks
		default:
			out = append(out, data[i:end]...)
		}
		i = end
		if kind == "IEND" { // nothing after IEND is kept
			return out, nil
		}
	}
	return nil, reject(ReasonImageUnreadable) // no IEND: truncated
}
