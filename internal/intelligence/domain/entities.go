package domain

import (
	"regexp"
	"sort"
	"strings"
)

// EntityType is generic on purpose (ADR-0017): no provider-specific types. It mirrors topic_entities.entity_type.
type EntityType string

const (
	EntityOrder        EntityType = "order"
	EntityInvoice      EntityType = "invoice"
	EntityContract     EntityType = "contract"
	EntitySubscription EntityType = "subscription"
	EntityProduct      EntityType = "product"
	EntityDevice       EntityType = "device"
	EntityTicket       EntityType = "ticket"
	EntityPayment      EntityType = "payment"
	EntityDocument     EntityType = "document"
	EntityService      EntityType = "service"
	EntityCustom       EntityType = "custom"
)

func (t EntityType) Valid() bool {
	switch t {
	case EntityOrder, EntityInvoice, EntityContract, EntitySubscription, EntityProduct, EntityDevice, EntityTicket, EntityPayment, EntityDocument, EntityService, EntityCustom:
		return true
	}
	return false
}

// DefinesSubject: the entity IS a subject of its own (an order, an invoice, a ticket...). A serial number, a product
// code or a document is an ATTRIBUTE that supplements whatever is being discussed ("o número de série é ABC123"): naming
// one that no topic holds yet is not evidence of a new subject.
func (t EntityType) DefinesSubject() bool {
	switch t {
	case EntityOrder, EntityInvoice, EntityContract, EntitySubscription, EntityTicket, EntityPayment, EntityService:
		return true
	}
	return false
}

// Label is how a topic about this entity is titled for a person.
func (t EntityType) Label() string {
	switch t {
	case EntityOrder:
		return "Pedido"
	case EntityInvoice:
		return "Nota fiscal"
	case EntityContract:
		return "Contrato"
	case EntityTicket:
		return "Chamado"
	case EntityDevice:
		return "Equipamento"
	case EntityPayment:
		return "Pagamento"
	case EntitySubscription:
		return "Assinatura"
	}
	return "Assunto"
}

// Entity is something a message names that identifies a subject ("pedido 837"). Key is canonical (digits, or upper
// case alphanumerics for serial numbers) so the same subject written differently matches.
type Entity struct {
	Type    EntityType
	Key     string
	Display string
	Offset  int // position in the text, to keep mention order
}

func (e Entity) String() string { return string(e.Type) + ":" + e.Key }

type entityRule struct {
	typ EntityType
	re  *regexp.Regexp
	// upper: keep letters (serial numbers); otherwise the capture is digits only
	upper bool
}

const num = `(?:n[ºo°]\.?\s*)?#?\s*`

var entityRules = []entityRule{
	{typ: EntityOrder, re: regexp.MustCompile(`(?i)\b(?:pedido|pedidos|ordem de servi[çc]o|o\.s\.)\s*` + num + `(\d{2,12})\b`)},
	{typ: EntityInvoice, re: regexp.MustCompile(`(?i)\b(?:nota fiscal|nf-?e|nfs-?e|nfe|nf|danfe|fatura|nota)\s*` + num + `(\d{3,12})\b`)},
	{typ: EntityPayment, re: regexp.MustCompile(`(?i)\b(?:boleto|pagamento|cobran[çc]a)\s*` + num + `(\d{3,12})\b`)},
	{typ: EntityContract, re: regexp.MustCompile(`(?i)\bcontrato\s*` + num + `(\d{2,12})\b`)},
	{typ: EntityTicket, re: regexp.MustCompile(`(?i)\b(?:chamado|ticket|protocolo|atendimento)\s*` + num + `(\d{2,12})\b`)},
	{typ: EntityDevice, upper: true, re: regexp.MustCompile(`(?i)\b(?:n[úu]mero de s[ée]rie|serial|s/n)\s*(?:[:=\-]|\s+é|\s+eh|\s+e)?\s*([A-Za-z0-9][A-Za-z0-9\-]{3,31})\b`)},
}

// ExtractEntities finds the subjects a text names, in order of appearance, without duplicates. It is a rule, not a
// guess: only an explicit keyword followed by an identifier counts.
func ExtractEntities(text string) []Entity {
	if len(text) > 8000 {
		text = text[:8000]
	}
	var found []Entity
	seen := map[string]bool{}
	for _, rule := range entityRules {
		for _, m := range rule.re.FindAllStringSubmatchIndex(text, -1) {
			key := text[m[2]:m[3]]
			if rule.upper {
				key = strings.ToUpper(key)
			}
			e := Entity{Type: rule.typ, Key: key, Display: strings.TrimSpace(text[m[0]:m[1]]), Offset: m[0]}
			if seen[e.String()] {
				continue
			}
			seen[e.String()] = true
			found = append(found, e)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Offset < found[j].Offset })
	return found
}
