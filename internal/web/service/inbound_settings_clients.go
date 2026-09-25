package service

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"gorm.io/gorm"
)

// Only fields belonging to this inbound's protocol are mirrored into its
// settings JSON. A shared client may also have WireGuard or other credentials;
// copying its whole global record into an unrelated inbound would disclose
// those secrets to that inbound's node.
var inboundClientSharedFields = []string{
	"email", "limitIp", "totalGB", "totalExhaustAction", "totalExhaustUpKbps",
	"totalExhaustDownKbps", "totalOverageMultiplierBps", "speedLimitKbps",
	"speedLimitUpKbps", "speedLimitDownKbps", "windowQuotaBytes", "windowHours",
	"windowMode", "windowExhaustAction", "windowExhaustUpKbps",
	"windowExhaustDownKbps", "windowOverageMultiplierBps", "expiryTime",
	"graceHours", "graceUpKbps", "graceDownKbps", "graceQuotaBytes",
	"enable", "tgId", "subId", "group", "comment", "reset", "resetDay",
	"resetMax", "trafficReset", "trafficResetDay", "created_at", "updated_at",
}

func inboundClientFields(protocol model.Protocol) []string {
	fields := append([]string(nil), inboundClientSharedFields...)
	switch protocol {
	case model.VLESS:
		return append(fields, "id", "flow", "reverse")
	case model.VMESS:
		return append(fields, "id", "security")
	case model.Trojan:
		return append(fields, "password", "flow")
	case model.Shadowsocks:
		return append(fields, "password")
	case model.Hysteria:
		return append(fields, "auth")
	case model.WireGuard, model.AmneziaWG:
		return append(fields, "privateKey", "publicKey", "allowedIPs", "preSharedKey", "keepAlive", "forwardedPorts")
	case model.MTProto:
		return append(fields, "secret", "adTag")
	case model.TUIC:
		return append(fields, "id", "password")
	default:
		return fields
	}
}

// reconcileInboundEditClients keeps the normalized client/link tables as the
// source of truth when an inbound's own settings are edited. The inbound form
// does not edit clients: it omits settings.clients and the current attached
// clients are inserted here. Older form/API payloads can include client entries
// without the newer policy fields; fill only missing keys from the normalized
// record so a harmless inbound rename cannot reset a client's advanced quota.
// An explicit clients array still controls membership and explicitly supplied
// client fields, including zero, still retain their update semantics.
func (s *InboundService) reconcileInboundEditClients(tx *gorm.DB, inbound *model.Inbound) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return err
	}
	if settings == nil {
		return common.NewError("inbound settings must be an object")
	}
	// Read the persisted settings inside the save transaction. A client edit
	// may have completed after UpdateInbound first loaded the old row, and the
	// stale snapshot must not put an old per-inbound credential back.
	var stored model.Inbound
	if err := tx.Model(&model.Inbound{}).Select("protocol", "settings").First(&stored, inbound.Id).Error; err != nil {
		return err
	}
	current, err := s.clientService.ListForInbound(tx, inbound.Id)
	if err != nil {
		return err
	}
	canonical := make(map[string]map[string]json.RawMessage, len(current))
	fields := inboundClientFields(inbound.Protocol)
	for i := range current {
		data, err := json.Marshal(current[i])
		if err != nil {
			return err
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(data, &entry); err != nil {
			return err
		}
		selected := make(map[string]json.RawMessage, len(fields))
		for _, key := range fields {
			if value, ok := entry[key]; ok {
				selected[key] = value
			}
		}
		canonical[strings.ToLower(strings.TrimSpace(current[i].Email))] = selected
	}

	clientJSON, supplied := settings["clients"]
	if !supplied {
		if len(current) > 0 && inbound.Protocol != stored.Protocol {
			return common.NewError("settings.clients is required when changing an inbound with attached clients to another protocol")
		}
		// Preserve protocol-specific fields not represented by the normalized
		// model, and keep WireGuard's per-inbound addresses/PSKs. Membership and
		// global client policy come from the current linked records.
		var oldSettings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stored.Settings), &oldSettings); err != nil {
			return err
		}
		if len(current) == 0 {
			if _, hadClients := oldSettings["clients"]; !hadClients {
				// Non-client protocols do not gain an extraneous clients key.
				return nil
			}
		}
		oldByEmail := make(map[string]map[string]json.RawMessage)
		if oldSettings != nil {
			oldEntries, err := decodeInboundClientEntries(oldSettings["clients"])
			if err != nil {
				return err
			}
			for _, entry := range oldEntries {
				oldByEmail[clientEntryEmail(entry)] = entry
			}
		}
		entries := make([]map[string]json.RawMessage, 0, len(current))
		for i := range current {
			email := strings.ToLower(strings.TrimSpace(current[i].Email))
			entry := make(map[string]json.RawMessage)
			for key, value := range oldByEmail[email] {
				entry[key] = value
			}
			for _, key := range fields {
				if value, ok := canonical[email][key]; ok {
					entry[key] = value
				} else {
					delete(entry, key)
				}
			}
			for _, key := range []string{"allowedIPs", "preSharedKey", "keepAlive", "forwardedPorts"} {
				if value, ok := oldByEmail[email][key]; ok {
					entry[key] = value
				}
			}
			entries = append(entries, entry)
		}
		clientJSON, err = json.Marshal(entries)
		if err != nil {
			return err
		}
	} else if string(clientJSON) != "null" {
		entries, err := decodeInboundClientEntries(clientJSON)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			for key, value := range canonical[clientEntryEmail(entry)] {
				if _, supplied := entry[key]; !supplied {
					entry[key] = value
				}
			}
		}
		clientJSON, err = json.Marshal(entries)
		if err != nil {
			return err
		}
	}
	settings["clients"] = clientJSON
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound.Settings = string(data)
	return nil
}

func decodeInboundClientEntries(raw json.RawMessage) ([]map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func clientEntryEmail(entry map[string]json.RawMessage) string {
	var email string
	_ = json.Unmarshal(entry["email"], &email)
	return strings.ToLower(strings.TrimSpace(email))
}

func ParseInboundSettingsClients(settings string) ([]model.Client, error) {
	trimmed := strings.TrimSpace(settings)
	if trimmed == "" || trimmed == "null" {
		return nil, common.NewError("inbound settings is empty")
	}

	var payload struct {
		Clients json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	if len(payload.Clients) == 0 || string(payload.Clients) == "null" {
		return nil, nil
	}

	var clients []model.Client
	if err := json.Unmarshal(payload.Clients, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

// settingsEntriesToClients decodes the wire entries a caller has already
// stamped, so a delta carries the persisted created_at / updated_at / subId
// rather than the pre-stamp values the request was parsed into.
func settingsEntriesToClients(entries []any) ([]model.Client, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	var clients []model.Client
	if err := json.Unmarshal(raw, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}
