// Package ids generates prefixed, fixed-width entity identifiers backed by
// per-type atomic DynamoDB counters and an Optimus reversible encoding.
package ids

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Optimus constants — ported verbatim from the Java order-service
// (OrderNumbers.java). One shared set; the 2-letter prefix namespaces values
// so reuse across entity types is safe.
const (
	optimusPrime   int64 = 1580030173
	optimusInverse int64 = 59260789
	optimusXor     int64 = 1163945558
	maxID          int64 = (1 << 31) - 1
	digitWidth           = 10
)

// EntityType binds an entity to its 2-letter ID prefix and DynamoDB counter key.
type EntityType struct {
	Prefix     string
	CounterKey string
}

var (
	User         = EntityType{Prefix: "US", CounterKey: "COUNTER!USER"}
	DE           = EntityType{Prefix: "DE", CounterKey: "COUNTER!DE"}
	Trip         = EntityType{Prefix: "TR", CounterKey: "COUNTER!TRIP"}
	Task         = EntityType{Prefix: "TK", CounterKey: "COUNTER!TASK"}
	Address      = EntityType{Prefix: "AD", CounterKey: "COUNTER!ADDRESS"}
	Dispute      = EntityType{Prefix: "DP", CounterKey: "COUNTER!DISPUTE"}
	Earning      = EntityType{Prefix: "EA", CounterKey: "COUNTER!EARNING"}
	Disbursement = EntityType{Prefix: "DB", CounterKey: "COUNTER!DISBURSEMENT"}
	CashDeposit  = EntityType{Prefix: "CD", CounterKey: "COUNTER!DEPOSIT"}
)

func encodeOptimus(n int64) int64 { return ((n * optimusPrime) & maxID) ^ optimusXor }
func decodeOptimus(e int64) int64 { return ((e ^ optimusXor) * optimusInverse) & maxID }

// Format builds the prefixed, zero-padded ID for counter value n (1..maxID).
func (t EntityType) Format(n int64) (string, error) {
	if n <= 0 || n > maxID {
		return "", fmt.Errorf("ids: counter value out of range: %d", n)
	}
	return fmt.Sprintf("%s%0*d", t.Prefix, digitWidth, encodeOptimus(n)), nil
}

// Decode reverses an ID produced by Format back to its counter value, after
// validating prefix, width, numeric payload, range, and round-trip.
func (t EntityType) Decode(id string) (int64, error) {
	if !strings.HasPrefix(id, t.Prefix) {
		return 0, fmt.Errorf("ids: %q missing prefix %q", id, t.Prefix)
	}
	digits := id[len(t.Prefix):]
	if len(digits) != digitWidth {
		return 0, fmt.Errorf("ids: %q has wrong digit width", id)
	}
	encoded, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ids: %q non-numeric payload: %w", id, err)
	}
	n := decodeOptimus(encoded)
	if n <= 0 || n > maxID {
		return 0, fmt.Errorf("ids: %q decodes out of range", id)
	}
	round, ferr := t.Format(n)
	if ferr != nil || round != id {
		return 0, fmt.Errorf("ids: %q failed round-trip", id)
	}
	return n, nil
}

// Counter yields the next monotonic integer for a counter key.
type Counter interface {
	NextValue(ctx context.Context, counterKey string) (int64, error)
}

// Generator produces prefixed IDs from a Counter.
type Generator struct{ counter Counter }

// NewGenerator builds a Generator backed by an atomic DynamoDB counter.
func NewGenerator(client *dynamodb.Client, tableName string) *Generator {
	return &Generator{counter: dynamoCounter{client: client, tableName: tableName}}
}

// NewGeneratorWithCounter injects a custom Counter (for tests).
func NewGeneratorWithCounter(c Counter) *Generator { return &Generator{counter: c} }

// NextID returns the next prefixed ID for t, or an error if the counter fails.
func (g *Generator) NextID(ctx context.Context, t EntityType) (string, error) {
	n, err := g.counter.NextValue(ctx, t.CounterKey)
	if err != nil {
		return "", fmt.Errorf("ids: counter %s: %w", t.CounterKey, err)
	}
	return t.Format(n)
}

// dynamoCounter implements Counter via an atomic ADD on a single counter item.
type dynamoCounter struct {
	client    *dynamodb.Client
	tableName string
}

func (d dynamoCounter) NextValue(ctx context.Context, counterKey string) (int64, error) {
	out, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: counterKey},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String("ADD seq :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": &types.AttributeValueMemberN{Value: "1"}},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, err
	}
	attr, ok := out.Attributes["seq"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("ids: counter %s missing seq attribute", counterKey)
	}
	n, err := strconv.ParseInt(attr.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ids: counter %s seq parse: %w", counterKey, err)
	}
	return n, nil
}
