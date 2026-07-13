package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

// JavaOrder is the subset of fields the cron needs from the Java order response.
type JavaOrder struct {
	OrderID     string          `json:"orderId"`
	OrderNumber string          `json:"orderNumber"`
	Status      string          `json:"status"`
	Delivery    JavaDelivery    `json:"delivery"`
	StoreID     string          `json:"storeId"` // may be empty; cron falls back to the store it is processing
	Items       []JavaOrderItem `json:"items"`

	PaymentMethod string  `json:"paymentMethod"`
	PaymentStatus string  `json:"paymentStatus"`
	GrandTotal    float64 `json:"grandTotal"`
	Currency      string  `json:"currency"`

	CustomerID string `json:"customerId"`
}

// EffectiveOrderID returns orderNumber when set (the stable human-readable key
// e.g. "ORD1162844363"), falling back to orderId if orderNumber is absent.
func (o JavaOrder) EffectiveOrderID() string {
	if n := strings.TrimSpace(o.OrderNumber); n != "" {
		return n
	}
	return strings.TrimSpace(o.OrderID)
}

// normalizeOrderID copies EffectiveOrderID into OrderID so downstream code and
// DynamoDB trip.order_id always have a stable lookup key.
func (o *JavaOrder) normalizeOrderID() {
	o.OrderID = o.EffectiveOrderID()
}

type JavaOrderItem struct {
	ProductName string `json:"productName"`
	ImageURL    string `json:"imageUrl"`
	Quantity    int    `json:"quantity"`
	Sku         string `json:"sku"`
}

type JavaDelivery struct {
	Address string  `json:"address"`
	Lat     float64 `json:"latitude"`
	Lng     float64 `json:"longitude"`
	Phone   string  `json:"phone"`
	Name    string  `json:"name"`
}

// orderServicePathPrefix is prepended to every order-service request path.
// The Java order-service is served behind this route prefix.
const orderServicePathPrefix = "/order-service"

type JavaOrderClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewJavaOrderClient(baseURL string, logger *logrus.Logger) *JavaOrderClient {
	return &JavaOrderClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// GetReadyForDeliveryOrders fetches all READY_FOR_DELIVERY orders for a store.
// Handles pagination — returns all pages combined.
func (c *JavaOrderClient) GetReadyForDeliveryOrders(ctx context.Context, storeID string) ([]JavaOrder, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetReadyForDeliveryOrders", logrus.Fields{"store_id": storeID})
	defer op.End()

	var allOrders []JavaOrder
	page := 0
	pageSize := 50

	for {
		url := fmt.Sprintf("%s%s/api/v1/orders/store/%s?status=%s&pageNum=%d&pageSize=%d",
			c.baseURL, orderServicePathPrefix, storeID, eligibleOrderStatus, page, pageSize)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to build request: %w", err))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, op.Fail(fmt.Errorf("java order-service returned %d", resp.StatusCode))
		}

		var paged struct {
			Content []JavaOrder `json:"content"`
			Meta    struct {
				Last bool `json:"last"`
			} `json:"meta"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&paged); err != nil {
			return nil, op.Fail(fmt.Errorf("failed to decode order response: %w", err))
		}

		for i := range paged.Content {
			paged.Content[i].normalizeOrderID()
		}
		allOrders = append(allOrders, paged.Content...)
		if paged.Meta.Last {
			break
		}
		page++
	}

	op.With("count", len(allOrders))
	return allOrders, nil
}

// GetOrderNotificationTarget fetches customerId for customer push notifications.
type OrderNotificationTarget struct {
	CustomerID  string `json:"customerId"`
	OrderUUID   string `json:"orderUuid"`
	OrderNumber string `json:"orderNumber"`
}

// GetNotificationTarget resolves the customer notification target for an order.
func (c *JavaOrderClient) GetNotificationTarget(ctx context.Context, orderRef string) (*OrderNotificationTarget, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetNotificationTarget", logrus.Fields{"order_ref": orderRef})
	defer op.End()

	url := fmt.Sprintf("%s%s/internal/v1/orders/%s/notification-target", c.baseURL, orderServicePathPrefix, orderRef)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to build request: %w", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, op.Fail(fmt.Errorf("java order-service returned %d", resp.StatusCode))
	}

	var target OrderNotificationTarget
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to decode notification target: %w", err))
	}
	return &target, nil
}

// UpdateOrderStatus calls Java to transition an order to a new status.
// actorID identifies who triggered the change (e.g. "DE:{deID}").
func (c *JavaOrderClient) UpdateOrderStatus(ctx context.Context, orderID, status, actorID string) error {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.UpdateOrderStatus", logrus.Fields{
		"order_id": orderID, "status": status,
	})
	defer op.End()

	url := fmt.Sprintf("%s%s/api/v1/orders/%s/status", c.baseURL, orderServicePathPrefix, orderID)
	body := fmt.Sprintf(`{"status":%q,"notes":"triggered by bunzo-qcom"}`, status)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return op.Fail(fmt.Errorf("failed to build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Actor-Id", actorID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return op.Fail(fmt.Errorf("java returned %d for order status update", resp.StatusCode))
	}
	return nil
}

// GetOrderStatus fetches only the status field of a single order.
// Used by the cron to detect cancellations on active trips.
func (c *JavaOrderClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetOrderStatus", logrus.Fields{"order_id": orderID})
	defer op.End()

	url := fmt.Sprintf("%s%s/api/v1/orders/%s", c.baseURL, orderServicePathPrefix, orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to build request: %w", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "NOT_FOUND", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", op.Fail(fmt.Errorf("java returned %d", resp.StatusCode))
	}

	var order struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return "", op.Fail(fmt.Errorf("failed to decode order: %w", err))
	}
	return order.Status, nil
}

// GetOrderRaw fetches the full order payload as a loosely-typed object so every
// field is preserved verbatim. Returns (nil, nil) when the order does not exist.
func (c *JavaOrderClient) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetOrderRaw", logrus.Fields{"order_id": orderID})
	defer op.End()

	url := fmt.Sprintf("%s%s/api/v1/orders/%s", c.baseURL, orderServicePathPrefix, orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to build request: %w", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, op.Fail(fmt.Errorf("java returned %d", resp.StatusCode))
	}

	var order map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to decode order: %w", err))
	}
	return order, nil
}
