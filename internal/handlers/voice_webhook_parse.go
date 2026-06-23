package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
)

type voiceCustomData struct {
	OrderID   string
	Direction string
}

type voiceWebhookPayload struct {
	UUID               string
	ConversationUUID   string
	From               string
	To                 string
	Status             string
	Duration           string
	CustomData         voiceCustomData
}

// parseVoiceWebhookBody normalizes Vonage Voice webhook payloads.
// SDK app calls send custom_data as a JSON-encoded string and use from_user
// instead of from on the answer webhook.
func parseVoiceWebhookBody(body []byte) (voiceWebhookPayload, error) {
	var raw struct {
		UUID             string          `json:"uuid"`
		ConversationUUID string          `json:"conversation_uuid"`
		From             string          `json:"from"`
		FromUser         string          `json:"from_user"`
		To               string          `json:"to"`
		Status           string          `json:"status"`
		Duration         string          `json:"duration"`
		CustomData       json.RawMessage `json:"custom_data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return voiceWebhookPayload{}, err
	}

	cd, err := parseVoiceCustomData(raw.CustomData)
	if err != nil {
		return voiceWebhookPayload{}, err
	}

	return voiceWebhookPayload{
		UUID:             strings.TrimSpace(raw.UUID),
		ConversationUUID: strings.TrimSpace(raw.ConversationUUID),
		From:             voiceCallerSub(raw.From, raw.FromUser, raw.To),
		To:               strings.TrimSpace(raw.To),
		Status:           strings.TrimSpace(raw.Status),
		Duration:         strings.TrimSpace(raw.Duration),
		CustomData:       cd,
	}, nil
}

func voiceCallerSub(from, fromUser, to string) string {
	if s := strings.TrimSpace(from); s != "" {
		return s
	}
	if s := strings.TrimSpace(fromUser); s != "" {
		return s
	}
	return strings.TrimSpace(to)
}

func parseVoiceCustomData(raw json.RawMessage) (voiceCustomData, error) {
	if len(raw) == 0 {
		return voiceCustomData{}, nil
	}

	var out voiceCustomData
	if err := unmarshalCustomDataObject(raw, &out); err == nil {
		return out, nil
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return voiceCustomData{}, fmt.Errorf("custom_data: %w", err)
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return voiceCustomData{}, nil
	}
	if err := unmarshalCustomDataObject([]byte(encoded), &out); err != nil {
		return voiceCustomData{}, fmt.Errorf("custom_data string: %w", err)
	}
	return out, nil
}

func unmarshalCustomDataObject(raw []byte, out *voiceCustomData) error {
	var obj struct {
		OrderID   string `json:"order_id"`
		Direction string `json:"direction"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	out.OrderID = strings.TrimSpace(obj.OrderID)
	out.Direction = strings.TrimSpace(obj.Direction)
	return nil
}

func customDataKeys(raw json.RawMessage) []string {
	cd, err := parseVoiceCustomData(raw)
	if err != nil || cd.OrderID == "" && cd.Direction == "" {
		return nil
	}
	var keys []string
	if cd.OrderID != "" {
		keys = append(keys, "order_id")
	}
	if cd.Direction != "" {
		keys = append(keys, "direction")
	}
	return keys
}
