//go:build smoke

package smoke

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// smokeDisputeCustomerPhone is the prod smoke-test customer (9515365236 with +91).
// Override with SMOKE_DISPUTE_CUSTOMER_PHONE.
const smokeDisputeCustomerPhone = "+919515365236"

var orderServiceURL string

func init() {
	orderServiceURL = strings.TrimRight(os.Getenv("SMOKE_ORDER_SERVICE_URL"), "/")
	if orderServiceURL == "" {
		orderServiceURL = "http://15.135.73.205:8082"
	}
}

var expectedDispositionCodes = []string{
	"ORDER_NOT_RECEIVED",
	"ITEMS_DIFFERENT",
	"DAMAGED_ITEMS",
	"PACKAGING_ISSUES",
	"EXPIRED_ITEMS",
	"ITEMS_MISSING",
	"BAD_QUALITY",
	"RETURN_ITEMS",
	"PAYMENT_REFUND",
}

// minimal JPEG (1x1) for presigned upload smoke tests.
var smokeDisputeJPEG = mustDecodeBase64(
	"/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//" +
		"2wBDAf//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//AP//" +
		"wAARCAABAAEDAREAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAA" +
		"AAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCwAA8A/9k=",
)

func mustDecodeBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("decode smoke JPEG: %v", err))
	}
	return b
}

func smokeDisputePhone() string {
	if p := strings.TrimSpace(os.Getenv("SMOKE_DISPUTE_CUSTOMER_PHONE")); p != "" {
		return p
	}
	return smokeDisputeCustomerPhone
}

func authenticateSmokeDisputeCustomer(t *testing.T) authResult {
	t.Helper()
	return authenticateCustomer(t, smokeDisputePhone())
}

func errorCode(result map[string]interface{}) string {
	errObj, _ := result["error"].(map[string]interface{})
	code, _ := errObj["code"].(string)
	return code
}

func doPut(t *testing.T, url, contentType string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT %s: %v", url, err)
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func presignDisputePhoto(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	resp, result := do(t, "POST", "/api/v1/uploads/url", bearer(token), map[string]interface{}{
		"use_case":  "dispute_photo",
		"file_name": "smoke-dispute.jpg",
		"file_type": "image/jpeg",
		"file_size": len(smokeDisputeJPEG),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("uploads/url: expected 200, got %d: %v", resp.StatusCode, result)
	}
	for _, key := range []string{"file_id", "upload_url", "object_key", "expires_in_seconds", "max_file_size"} {
		if v, _ := result[key].(string); key != "max_file_size" && key != "expires_in_seconds" && v == "" {
			if result[key] == nil {
				t.Fatalf("uploads/url missing %q", key)
			}
		}
	}
	return result
}

func uploadSmokeDisputePhoto(t *testing.T, token, customerID string) string {
	t.Helper()
	result := presignDisputePhoto(t, token)
	uploadURL, _ := result["upload_url"].(string)
	objectKey, _ := result["object_key"].(string)
	if uploadURL == "" || objectKey == "" {
		t.Fatalf("uploads/url: missing upload_url or object_key: %v", result)
	}
	wantPrefix := "disputes/" + customerID + "/"
	if !strings.HasPrefix(objectKey, wantPrefix) {
		t.Fatalf("object_key %q should start with %q", objectKey, wantPrefix)
	}
	status := doPut(t, uploadURL, "image/jpeg", smokeDisputeJPEG)
	if status < 200 || status >= 300 {
		t.Fatalf("S3 PUT: expected 2xx, got %d", status)
	}
	return objectKey
}

type customerOrder struct {
	OrderNumber string
	Status      string
}

func fetchCustomerOrders(t *testing.T, customerID string, pageNum, pageSize int) []customerOrder {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/orders/customer/%s?pageNum=%d&pageSize=%d",
		orderServiceURL, customerID, pageNum, pageSize)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("order-service GET customer orders: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("order-service: expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode order-service response: %v", err)
	}
	raw, _ := payload["content"].([]interface{})
	out := make([]customerOrder, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]interface{})
		if m == nil {
			continue
		}
		out = append(out, customerOrder{
			OrderNumber: fmt.Sprint(m["orderNumber"]),
			Status:      fmt.Sprint(m["status"]),
		})
	}
	return out
}

// findDisputableOrder returns a DELIVERED order with no open dispute, or ("", false).
func findDisputableOrder(t *testing.T, token, customerID string) (string, bool) {
	t.Helper()
	for page := 0; page < 5; page++ {
		orders := fetchCustomerOrders(t, customerID, page, 20)
		if len(orders) == 0 {
			break
		}
		for _, o := range orders {
			if o.Status != "DELIVERED" || o.OrderNumber == "" {
				continue
			}
			resp, result := do(t, "GET",
				"/api/v1/disputes/by-order?order_number="+o.OrderNumber,
				bearer(token), nil)
			if resp.StatusCode == http.StatusNotFound {
				if code := errorCode(result); code == "" || code == "DISPUTE_NOT_FOUND" {
					return o.OrderNumber, true
				}
			}
		}
		if len(orders) < 20 {
			break
		}
	}
	return "", false
}

// findExistingDisputeOrder returns a DELIVERED order that already has a dispute.
func findExistingDisputeOrder(t *testing.T, token, customerID string) (string, string, bool) {
	t.Helper()
	for page := 0; page < 5; page++ {
		orders := fetchCustomerOrders(t, customerID, page, 20)
		if len(orders) == 0 {
			break
		}
		for _, o := range orders {
			if o.Status != "DELIVERED" || o.OrderNumber == "" {
				continue
			}
			resp, result := do(t, "GET",
				"/api/v1/disputes/by-order?order_number="+o.OrderNumber,
				bearer(token), nil)
			if resp.StatusCode != http.StatusOK {
				continue
			}
			dispute, _ := result["dispute"].(map[string]interface{})
			id, _ := dispute["dispute_id"].(string)
			if id != "" {
				return o.OrderNumber, id, true
			}
		}
		if len(orders) < 20 {
			break
		}
	}
	return "", "", false
}

func TestSmoke_DisputeDispositions(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)
	resp, result := do(t, "GET", "/api/v1/disputes/dispositions", bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	items, ok := result["dispositions"].([]interface{})
	if !ok {
		t.Fatalf("missing dispositions array: %v", result)
	}
	if len(items) != len(expectedDispositionCodes) {
		t.Fatalf("expected %d dispositions, got %d", len(expectedDispositionCodes), len(items))
	}
	seen := map[string]bool{}
	for i, raw := range items {
		d, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("disposition[%d] is not an object", i)
		}
		code, _ := d["code"].(string)
		title, _ := d["title"].(string)
		if code == "" || title == "" {
			t.Fatalf("disposition[%d] missing code/title: %v", i, d)
		}
		if _, ok := d["photos_required"].(bool); !ok {
			t.Fatalf("disposition %q missing photos_required", code)
		}
		if _, ok := d["description_required"].(bool); !ok {
			t.Fatalf("disposition %q missing description_required", code)
		}
		if _, ok := d["photo_min"].(float64); !ok {
			t.Fatalf("disposition %q missing photo_min", code)
		}
		seen[code] = true
	}
	for _, code := range expectedDispositionCodes {
		if !seen[code] {
			t.Fatalf("missing disposition code %q", code)
		}
	}
}

func TestSmoke_DisputeAuthErrors(t *testing.T) {
	resp, _ := do(t, "GET", "/api/v1/disputes/dispositions", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dispositions without auth: expected 401, got %d", resp.StatusCode)
	}

	dePhone := smokePhone()
	registerDE(t, dePhone)
	deAuth := authenticateDE(t, dePhone)
	resp, result := do(t, "GET", "/api/v1/disputes/dispositions", bearer(deAuth.AccessToken), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DE on dispositions: expected 403, got %d: %v", resp.StatusCode, result)
	}
}

func TestSmoke_DisputeUploadPresignValidation(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)

	resp, result := do(t, "POST", "/api/v1/uploads/url", bearer(auth.AccessToken), map[string]interface{}{
		"use_case":  "not_a_real_use_case",
		"file_name": "x.jpg",
		"file_type": "image/jpeg",
		"file_size": 100,
	})
	if resp.StatusCode != http.StatusBadRequest || errorCode(result) != "UNKNOWN_USE_CASE" {
		t.Fatalf("unknown use_case: expected 400 UNKNOWN_USE_CASE, got %d %v", resp.StatusCode, result)
	}

	resp, result = do(t, "POST", "/api/v1/uploads/url", bearer(auth.AccessToken), map[string]interface{}{
		"use_case":  "dispute_photo",
		"file_name": "x.pdf",
		"file_type": "application/pdf",
		"file_size": 100,
	})
	if resp.StatusCode != http.StatusBadRequest || errorCode(result) != "MIME_NOT_ALLOWED" {
		t.Fatalf("bad mime: expected 400 MIME_NOT_ALLOWED, got %d %v", resp.StatusCode, result)
	}
}

func TestSmoke_DisputePhotoUpload(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)
	uploadSmokeDisputePhoto(t, auth.AccessToken, auth.EntityID)
}

func TestSmoke_DisputeCreateWithPhoto(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)
	orderNumber, ok := findDisputableOrder(t, auth.AccessToken, auth.EntityID)
	if !ok {
		t.Skip("no DELIVERED order without an existing dispute for smoke customer")
	}
	objectKey := uploadSmokeDisputePhoto(t, auth.AccessToken, auth.EntityID)

	resp, result := do(t, "POST", "/api/v1/disputes", bearer(auth.AccessToken), map[string]interface{}{
		"order_number":     orderNumber,
		"disposition_code": "DAMAGED_ITEMS",
		"description":      "Smoke test dispute with dummy photo attachment.",
		"photo_keys":       []string{objectKey},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create dispute: expected 201, got %d: %v", resp.StatusCode, result)
	}
	dispute, ok := result["dispute"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing dispute in create response: %v", result)
	}
	disputeID, _ := dispute["dispute_id"].(string)
	if disputeID == "" {
		t.Fatal("missing dispute_id")
	}
	if dispute["status"].(string) != "OPEN" {
		t.Fatalf("expected status OPEN, got %v", dispute["status"])
	}
	if dispute["order_number"].(string) != orderNumber {
		t.Fatalf("order_number mismatch: got %v want %s", dispute["order_number"], orderNumber)
	}
	keys, _ := dispute["photo_keys"].([]interface{})
	if len(keys) != 1 || keys[0].(string) != objectKey {
		t.Fatalf("photo_keys mismatch: got %v want [%s]", keys, objectKey)
	}

	resp, result = do(t, "GET", "/api/v1/disputes/"+disputeID, bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by id: expected 200, got %d: %v", resp.StatusCode, result)
	}
	got, _ := result["dispute"].(map[string]interface{})
	if got["dispute_id"].(string) != disputeID {
		t.Fatal("get by id: dispute_id mismatch")
	}

	resp, result = do(t, "GET",
		"/api/v1/disputes/by-order?order_number="+orderNumber,
		bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by order: expected 200, got %d: %v", resp.StatusCode, result)
	}
	byOrder, _ := result["dispute"].(map[string]interface{})
	if byOrder["dispute_id"].(string) != disputeID {
		t.Fatal("get by order: dispute_id mismatch")
	}
}

func TestSmoke_DisputeReadExisting(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)
	orderNumber, disputeID, ok := findExistingDisputeOrder(t, auth.AccessToken, auth.EntityID)
	if !ok {
		t.Skip("no existing dispute on a DELIVERED order for smoke customer")
	}

	resp, result := do(t, "GET", "/api/v1/disputes/"+disputeID, bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by id: expected 200, got %d: %v", resp.StatusCode, result)
	}

	resp, result = do(t, "GET",
		"/api/v1/disputes/by-order?order_number="+orderNumber,
		bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by order: expected 200, got %d: %v", resp.StatusCode, result)
	}
	got, _ := result["dispute"].(map[string]interface{})
	if got["dispute_id"].(string) != disputeID {
		t.Fatalf("by-order dispute_id: got %v want %s", got["dispute_id"], disputeID)
	}
}

func TestSmoke_DisputeCreateValidation(t *testing.T) {
	auth := authenticateSmokeDisputeCustomer(t)

	resp, result := do(t, "POST", "/api/v1/disputes", bearer(auth.AccessToken), map[string]interface{}{
		"disposition_code": "DAMAGED_ITEMS",
	})
	if resp.StatusCode != http.StatusBadRequest || errorCode(result) != "MISSING_FIELD" {
		t.Fatalf("missing order_number: expected 400 MISSING_FIELD, got %d %v", resp.StatusCode, result)
	}

	orders := fetchCustomerOrders(t, auth.EntityID, 0, 20)
	var deliveredOrder string
	for _, o := range orders {
		if o.Status == "DELIVERED" && o.OrderNumber != "" {
			deliveredOrder = o.OrderNumber
			break
		}
	}
	if deliveredOrder == "" {
		t.Skip("no DELIVERED order available for disposition validation smoke test")
	}

	resp, result = do(t, "POST", "/api/v1/disputes", bearer(auth.AccessToken), map[string]interface{}{
		"order_number":     deliveredOrder,
		"disposition_code": "NOT_A_REAL_CODE",
	})
	if resp.StatusCode != http.StatusBadRequest || errorCode(result) != "DISPOSITION_NOT_FOUND" {
		t.Fatalf("bad disposition: expected 400 DISPOSITION_NOT_FOUND, got %d %v", resp.StatusCode, result)
	}
}
