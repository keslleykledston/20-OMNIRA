package domain

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ParserVersion identifies the export parser; it is stored with every import so a parse is reproducible.
const ParserVersion = "wa-export-1"

const (
	// MaxImportBytes is the largest export accepted (and the CHECK of wa_group_imports.size_bytes).
	MaxImportBytes = 50 * 1024 * 1024
	// MaxExportEntries / MaxExportRecords bound what a hostile or huge file may make the server do.
	MaxExportEntries = 100
	MaxExportRecords = 250000
	// maxMessageRunes bounds one message; the model never needs more and the parse must not be a memory amplifier.
	maxMessageRunes = 8000
	// zipRatioLimit: an entry that inflates more than this is a zip bomb.
	zipRatioLimit = 100
)

var (
	ErrImportTooLarge  = errors.New("groupai: the file is too large")
	ErrImportMalformed = errors.New("groupai: the file is not a WhatsApp conversation export")
	ErrImportZip       = errors.New("groupai: the zip file is not acceptable")
)

type ParsedMessage struct {
	At     time.Time // UTC
	Author string
	Text   string
	Media  bool // an attachment: only the marker is kept, never the file
	Line   int
}

type ParseResult struct {
	Messages  []ParsedMessage
	Total     int            // every dated record of the file, system lines included
	Rejected  map[string]int // reason -> count (system, deleted, empty, bad_date)
	Truncated int            // messages cut to maxMessageRunes
	Unparsed  int            // lines before the first dated record
	DateOrder string         // dmy or mdy, as detected
	From, To  *time.Time
}

func (r ParseResult) RejectedTotal() int {
	n := 0
	for _, v := range r.Rejected {
		n += v
	}
	return n
}

// The two header shapes of a WhatsApp export:
//
//	Android: "12/03/2024 14:35 - João: texto"   (also "12/03/24, 2:35 PM - ...")
//	iOS:     "[12/03/2024 14:35:10] João: texto" (also "[12/03/24, 2:35:10 PM] ...")
var (
	androidHeader = regexp.MustCompile(`^(\d{1,2})[/.](\d{1,2})[/.](\d{2,4}),? (\d{1,2}):(\d{2})(?::(\d{2}))? ?([AaPp]\.?[Mm]\.?)? ?[-–—] (.*)$`)
	iosHeader     = regexp.MustCompile(`^\[(\d{1,2})[/.](\d{1,2})[/.](\d{2,4}),? (\d{1,2}):(\d{2})(?::(\d{2}))? ?([AaPp]\.?[Mm]\.?)?\] ?(.*)$`)

	mediaOnly    = regexp.MustCompile(`(?i)^(<m[íi]dia oculta>|<media omitted>|<arquivo de m[íi]dia oculto>|<anexado: .*>|<attached: .*>|(imagem|[áa]udio|v[íi]deo|figurinha|sticker|documento|gif|contato|image|audio|video|document) (omitida|ocultada|omitido|ocultado|omitted)|localiza[çc][ãa]o: https?://\S+)$`)
	attachedTail = regexp.MustCompile(`(?i)\s*\((arquivo anexado|file attached)\)\s*$`)
	deletedMsg   = regexp.MustCompile(`(?i)^(esta mensagem foi apagada|voc[êe] apagou esta mensagem|mensagem apagada|this message was deleted|you deleted this message)\.?$`)
	invisibles   = strings.NewReplacer("\u200e", "", "\u200f", "", "\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "", "\ufeff", "", "\u202f", " ", "\u00a0", " ")
)

type rawRecord struct {
	line                   int
	a, b, year, hh, mm, ss int
	ampm                   string
	rest                   string
}

// ParseWhatsAppExport reads an exported chat (the text of the .txt) into messages. It declares what it could not understand instead of
// guessing: a system line, a deleted message, an empty one and an impossible date are counted in Rejected with their reason. Times are
// those of the phone that exported, read in loc, and returned in UTC. The day/month order is detected from the data (a number above
// 12 proves it) and defaults to day first.
func ParseWhatsAppExport(text string, loc *time.Location) (ParseResult, error) {
	if loc == nil {
		loc = time.UTC
	}
	res := ParseResult{Rejected: map[string]int{}}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")

	var recs []rawRecord
	for n, line := range strings.Split(text, "\n") {
		clean := invisibles.Replace(line)
		rec, ok := matchHeader(clean, n+1)
		if ok {
			if len(recs) >= MaxExportRecords {
				return res, fmt.Errorf("%w: more than %d messages", ErrImportTooLarge, MaxExportRecords)
			}
			recs = append(recs, rec)
			continue
		}
		if len(recs) == 0 {
			if strings.TrimSpace(clean) != "" {
				res.Unparsed++
			}
			continue
		}
		recs[len(recs)-1].rest += "\n" + clean // a continuation of the previous message
	}
	if len(recs) == 0 {
		return res, fmt.Errorf("%w: no dated message found", ErrImportMalformed)
	}
	res.Total = len(recs)

	order := detectOrder(recs)
	res.DateOrder = order
	for _, r := range recs {
		day, month := r.a, r.b
		if order == "mdy" {
			day, month = r.b, r.a
		}
		at, ok := buildTime(r, day, month, loc)
		if !ok {
			res.Rejected["bad_date"]++
			continue
		}
		author, body, isMsg := splitAuthor(r.rest)
		if !isMsg {
			res.Rejected["system"]++
			continue
		}
		msg := ParsedMessage{At: at.UTC(), Author: author, Line: r.line}
		body = strings.TrimSpace(body)
		switch {
		case deletedMsg.MatchString(body):
			res.Rejected["deleted"]++
			continue
		case mediaOnly.MatchString(body):
			msg.Media = true
		default:
			if attachedTail.MatchString(firstLine(body)) {
				msg.Media = true
				body = strings.TrimSpace(attachedTail.ReplaceAllString(firstLine(body), "") + strings.TrimPrefix(body, firstLine(body)))
				if looksLikeFilename(body) {
					body = ""
				}
			}
			msg.Text = body
		}
		if !msg.Media && strings.TrimSpace(msg.Text) == "" {
			res.Rejected["empty"]++
			continue
		}
		if rn := []rune(msg.Text); len(rn) > maxMessageRunes {
			msg.Text = string(rn[:maxMessageRunes])
			res.Truncated++
		}
		res.Messages = append(res.Messages, msg)
		if res.From == nil || msg.At.Before(*res.From) {
			t := msg.At
			res.From = &t
		}
		if res.To == nil || msg.At.After(*res.To) {
			t := msg.At
			res.To = &t
		}
	}
	if len(res.Messages) == 0 {
		return res, fmt.Errorf("%w: no readable message", ErrImportMalformed)
	}
	return res, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func looksLikeFilename(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \n") {
		return s == ""
	}
	ext := strings.ToLower(path.Ext(s))
	return ext != "" && len(ext) <= 6
}

func matchHeader(line string, n int) (rawRecord, bool) {
	var m []string
	if m = androidHeader.FindStringSubmatch(line); m == nil {
		m = iosHeader.FindStringSubmatch(line)
	}
	if m == nil {
		return rawRecord{}, false
	}
	atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return rawRecord{line: n, a: atoi(m[1]), b: atoi(m[2]), year: atoi(m[3]), hh: atoi(m[4]), mm: atoi(m[5]), ss: atoi(m[6]),
		ampm: strings.ToLower(strings.ReplaceAll(m[7], ".", "")), rest: m[8]}, true
}

func detectOrder(recs []rawRecord) string {
	firstOver, secondOver := false, false
	for _, r := range recs {
		if r.a > 12 {
			firstOver = true
		}
		if r.b > 12 {
			secondOver = true
		}
	}
	if secondOver && !firstOver {
		return "mdy"
	}
	return "dmy"
}

func buildTime(r rawRecord, day, month int, loc *time.Location) (time.Time, bool) {
	year := r.year
	if year < 100 {
		year += 2000
	}
	hh := r.hh
	switch r.ampm {
	case "pm":
		if hh < 12 {
			hh += 12
		}
	case "am":
		if hh == 12 {
			hh = 0
		}
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hh > 23 || r.mm > 59 || r.ss > 59 || year < 2009 || year > 2100 {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hh, r.mm, r.ss, 0, loc)
	if t.Day() != day { // 31 of a 30-day month rolled over
		return time.Time{}, false
	}
	return t, true
}

// splitAuthor separates "Author: text". A line without an author is a system line (someone joined, the subject changed, encryption
// notice). The author is short and cannot contain quotes: a subject change that happens to contain ": " is not a person.
func splitAuthor(rest string) (author, text string, isMessage bool) {
	i := strings.Index(rest, ": ")
	if i <= 0 || i > 100 {
		return "", "", false
	}
	author = strings.TrimSpace(rest[:i])
	if author == "" || strings.ContainsAny(author, "\n\"“”") {
		return "", "", false
	}
	return author, rest[i+2:], true
}

// ExtractExportText returns the chat text of an uploaded file: the file itself when it is text, or the chat .txt inside a zip. The zip is
// never extracted to disk and nothing but the one text entry is read - media inside it is ignored. Limits stop a zip bomb.
func ExtractExportText(data []byte) (text string, format string, err error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("%w: empty file", ErrImportMalformed)
	}
	if len(data) > MaxImportBytes {
		return "", "", ErrImportTooLarge
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		if bytes.IndexByte(data, 0) >= 0 {
			return "", "", fmt.Errorf("%w: binary content", ErrImportMalformed)
		}
		return string(data), "whatsapp_txt", nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("%w: unreadable archive", ErrImportZip)
	}
	if len(zr.File) > MaxExportEntries {
		return "", "", fmt.Errorf("%w: too many entries", ErrImportZip)
	}
	var total uint64
	var chosen *zip.File
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > uint64(MaxImportBytes)*4 {
			return "", "", fmt.Errorf("%w: the archive inflates too much", ErrImportZip)
		}
		if f.UncompressedSize64 > 1<<20 && f.UncompressedSize64 > f.CompressedSize64*zipRatioLimit {
			return "", "", fmt.Errorf("%w: suspicious compression ratio", ErrImportZip)
		}
		name := strings.ToLower(path.Base(f.Name))
		if f.FileInfo().IsDir() || !strings.HasSuffix(name, ".txt") || f.UncompressedSize64 > MaxImportBytes {
			continue
		}
		if chosen == nil || betterChat(name, strings.ToLower(path.Base(chosen.Name))) {
			chosen = f
		}
	}
	if chosen == nil {
		return "", "", fmt.Errorf("%w: no chat text inside the archive", ErrImportZip)
	}
	rc, err := chosen.Open()
	if err != nil {
		return "", "", fmt.Errorf("%w: unreadable entry", ErrImportZip)
	}
	defer rc.Close()
	buf, err := io.ReadAll(io.LimitReader(rc, MaxImportBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("%w: unreadable entry", ErrImportZip)
	}
	if len(buf) > MaxImportBytes {
		return "", "", ErrImportTooLarge
	}
	return string(buf), "whatsapp_zip", nil
}

func betterChat(candidate, current string) bool {
	score := func(n string) int {
		s := 0
		if n == "_chat.txt" {
			s += 3
		}
		if strings.Contains(n, "chat") || strings.Contains(n, "conversa") {
			s++
		}
		return s
	}
	return score(candidate) > score(current)
}

// ---- normalized form (the JSONL stored next to the raw, so a reprocessing never parses the export again)

type normalizedLine struct {
	T string `json:"t"`
	A string `json:"a"`
	X string `json:"x,omitempty"`
	M bool   `json:"m,omitempty"`
}

func EncodeNormalized(msgs []ParsedMessage) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, m := range msgs {
		_ = enc.Encode(normalizedLine{T: m.At.UTC().Format(time.RFC3339), A: m.Author, X: m.Text, M: m.Media})
	}
	return b.Bytes()
}

func DecodeNormalized(data []byte) ([]ParsedMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out []ParsedMessage
	for i := 1; ; i++ {
		var l normalizedLine
		if err := dec.Decode(&l); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, fmt.Errorf("%w: normalized line %d", ErrImportMalformed, i)
		}
		t, err := time.Parse(time.RFC3339, l.T)
		if err != nil {
			return nil, fmt.Errorf("%w: normalized line %d", ErrImportMalformed, i)
		}
		out = append(out, ParsedMessage{At: t.UTC(), Author: l.A, Text: l.X, Media: l.M, Line: i})
	}
}

// MessageKey identifies a message across a live capture and an export, which has no provider id and only minute precision: the author,
// the minute and the words. The same message in both is the same key.
func MessageKey(author string, at time.Time, text string) string {
	sum := sha256.Sum256([]byte(normalizeWords(author) + "|" + at.UTC().Truncate(time.Minute).Format(time.RFC3339) + "|" + normalizeWords(text)))
	return hex.EncodeToString(sum[:])
}
