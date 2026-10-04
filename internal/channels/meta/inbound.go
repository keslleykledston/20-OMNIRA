package meta

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/channels/domain"
)

// Event is one canonical webhook effect extracted from a Meta payload. Exactly
// one of Message or Status is set. Key is the provider id (wamid) used for
// idempotent intake.
type Event struct {
	Key     string
	Type    string
	Message *domain.InboundMessage
	Status  *domain.DeliveryStatusUpdate
}

type mediaRef struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
}

type payload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From      string `json:"from"`
					ID        string `json:"id"`
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					// Context is present when the customer replied to a specific message (context.id is its wamid).
					Context struct {
						ID string `json:"id"`
					} `json:"context"`
					Text        struct{ Body string } `json:"text"`
					Image       *mediaRef             `json:"image"`
					Video       *mediaRef             `json:"video"`
					Audio       *mediaRef             `json:"audio"`
					Document    *mediaRef             `json:"document"`
					Sticker     *mediaRef             `json:"sticker"`
					Button      struct{ Text string } `json:"button"`
					Interactive struct {
						ButtonReply struct{ Title string } `json:"button_reply"`
						ListReply   struct{ Title string } `json:"list_reply"`
					} `json:"interactive"`
				} `json:"messages"`
				Statuses []struct {
					ID        string `json:"id"`
					Status    string `json:"status"`
					Timestamp string `json:"timestamp"`
					Errors    []struct {
						Code  int    `json:"code"`
						Title string `json:"title"`
					} `json:"errors"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// ParseEvents translates a signature-verified Meta payload into canonical
// events. It never reads tenant information from the payload; the connection
// is resolved beforehand from phone_number_id. Unsupported message types are
// skipped, not failed, so Meta does not retry them forever.
func ParseEvents(conn domain.ChannelConnection, body []byte) ([]Event, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("meta: malformed payload: %w", err)
	}
	var events []Event
	for _, entry := range p.Entry {
		for _, change := range entry.Changes {
			for _, m := range change.Value.Messages {
				if m.ID == "" || m.From == "" {
					continue
				}
				msg := &domain.InboundMessage{
					ProviderMessageID: m.ID,
					ConnectionID:      conn.ID.String(),
					FromE164:          "+" + strings.TrimPrefix(m.From, "+"),
					Timestamp:         parseUnix(m.Timestamp),
					// For the Cloud API the sender id is the customer's wa_id (digits), the same value as "from".
					ParticipantID:     metaParticipantID(m.From),
					ReplyToExternalID: metaReplyID(m.Context.ID),
				}
				switch m.Type {
				case "text":
					msg.Text = m.Text.Body
				case "button":
					msg.Text = m.Button.Text
				case "interactive":
					msg.Text = m.Interactive.ButtonReply.Title
					if msg.Text == "" {
						msg.Text = m.Interactive.ListReply.Title
					}
				case "image":
					msg.Media = media(domain.MediaKindImage, m.Image)
				case "video":
					msg.Media = media(domain.MediaKindVideo, m.Video)
				case "audio":
					msg.Media = media(domain.MediaKindAudio, m.Audio)
				case "document":
					msg.Media = media(domain.MediaKindDocument, m.Document)
				case "sticker":
					msg.Media = media(domain.MediaKindSticker, m.Sticker)
				default:
					continue
				}
				if msg.Text == "" && msg.Media == nil {
					continue
				}
				events = append(events, Event{Key: m.ID, Type: "message", Message: msg})
			}
			for _, s := range change.Value.Statuses {
				state, ok := deliveryState(s.Status)
				if !ok || s.ID == "" {
					continue
				}
				u := &domain.DeliveryStatusUpdate{ProviderMessageID: s.ID, State: state, OccurredAt: parseUnix(s.Timestamp)}
				if state == domain.DeliveryStateFailed && len(s.Errors) > 0 {
					u.Reason = fmt.Sprintf("%d: %s", s.Errors[0].Code, s.Errors[0].Title)
				}
				events = append(events, Event{Key: s.ID + ":" + s.Status, Type: "status", Status: u})
			}
		}
	}
	return events, nil
}

func media(kind domain.MediaKind, r *mediaRef) *domain.InboundMedia {
	if r == nil || r.ID == "" {
		return nil
	}
	return &domain.InboundMedia{Kind: kind, MediaRef: r.ID, MimeType: r.MimeType}
}

func deliveryState(s string) (domain.DeliveryState, bool) {
	switch s {
	case "sent":
		return domain.DeliveryStateSent, true
	case "delivered":
		return domain.DeliveryStateDelivered, true
	case "read":
		return domain.DeliveryStateRead, true
	case "failed":
		return domain.DeliveryStateFailed, true
	}
	return "", false
}

func parseUnix(s string) time.Time {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || sec <= 0 {
		return time.Now().UTC()
	}
	return time.Unix(sec, 0).UTC()
}

func metaParticipantID(from string) string {
	from = strings.TrimPrefix(strings.TrimSpace(from), "+")
	if from == "" || len(from) > 32 {
		return ""
	}
	for _, r := range from {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return from
}

func metaReplyID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 300 {
		return ""
	}
	for _, r := range id {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return id
}
