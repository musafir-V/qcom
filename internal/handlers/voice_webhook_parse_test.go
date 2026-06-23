package handlers

import "testing"

func TestParseVoiceWebhookBody_VonageAppAnswer(t *testing.T) {
	body := []byte(`{
		"endpoint_type":"app",
		"from_user":"cust_15d02021-b59f-4470-b649-cca22844dd8c",
		"uuid":"81884205-6d26-4555-bdc6-2470d0a6bcaa",
		"custom_data":"{\"order_id\":\"ORD0565919100\",\"direction\":\"cust_to_de\"}"
	}`)

	got, err := parseVoiceWebhookBody(body)
	if err != nil {
		t.Fatalf("parseVoiceWebhookBody: %v", err)
	}
	if got.From != "cust_15d02021-b59f-4470-b649-cca22844dd8c" {
		t.Fatalf("From = %q", got.From)
	}
	if got.CustomData.OrderID != "ORD0565919100" {
		t.Fatalf("OrderID = %q", got.CustomData.OrderID)
	}
	if got.CustomData.Direction != "cust_to_de" {
		t.Fatalf("Direction = %q", got.CustomData.Direction)
	}
}

func TestParseVoiceWebhookBody_ObjectCustomData(t *testing.T) {
	body := []byte(`{"from":"de_D1","custom_data":{"order_id":"O1","direction":"de_to_cust"}}`)

	got, err := parseVoiceWebhookBody(body)
	if err != nil {
		t.Fatalf("parseVoiceWebhookBody: %v", err)
	}
	if got.From != "de_D1" {
		t.Fatalf("From = %q", got.From)
	}
	if got.CustomData.OrderID != "O1" {
		t.Fatalf("OrderID = %q", got.CustomData.OrderID)
	}
}

func TestParseVoiceCustomData_StringForm(t *testing.T) {
	raw := []byte(`"{\"order_id\":\"ORD1\",\"direction\":\"cust_to_de\"}"`)
	got, err := parseVoiceCustomData(raw)
	if err != nil {
		t.Fatalf("parseVoiceCustomData: %v", err)
	}
	if got.OrderID != "ORD1" || got.Direction != "cust_to_de" {
		t.Fatalf("got %+v", got)
	}
}
