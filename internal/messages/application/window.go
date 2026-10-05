package application

import "time"

// ProviderMetaCloud is the provider whose WhatsApp rules limit free text to a 24 h window after the customer's
// last message. WAHA (unofficial) has no such rule.
const ProviderMetaCloud = "meta_cloud"

// SessionWindowDuration is WhatsApp's customer-service window.
const SessionWindowDuration = 24 * time.Hour

// SessionWindow tells whether free text may be sent now. required=false means the provider has no window, so it is
// always open. For a windowed provider it is open only within 24 h of the customer's last inbound message.
func SessionWindow(provider string, lastInbound *time.Time, now time.Time) (required, open bool) {
	if provider != ProviderMetaCloud {
		return false, true
	}
	if lastInbound == nil {
		return true, false
	}
	return true, now.Sub(*lastInbound) < SessionWindowDuration
}
