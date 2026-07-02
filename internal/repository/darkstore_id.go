package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	darkstoreCounterPK  = "COUNTER!DARKSTORE"
	darkstoreCounterSK  = "METADATA"
	darkstoreIDMinWidth = 3
)

// nextDarkstoreCounter atomically increments the dedicated darkstore counter
// item and returns the new value. Uses its own counter key — deliberately NOT
// the shared internal/ids package, whose EntityType.Format() always
// Optimus-obfuscates into a 12-char code. Darkstore IDs must stay a short,
// visibly sequential number (e.g. "221") since ops staff read/write it on
// physical store signage.
func nextDarkstoreCounter(ctx context.Context, client *dynamodb.Client, tableName string) (int64, error) {
	out, err := client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: darkstoreCounterPK},
			"SK": &types.AttributeValueMemberS{Value: darkstoreCounterSK},
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
		return 0, fmt.Errorf("darkstore counter missing seq attribute")
	}
	n, err := strconv.ParseInt(attr.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("darkstore counter seq parse: %w", err)
	}
	return n, nil
}

// formatDarkstoreID zero-pads n to at least 3 digits ("1" -> "001"). Past 999
// it widens rather than erroring or wrapping ("1000" -> "1000") — a soft cap,
// not a hard one, so the 1000th store is still creatable with no code change.
func formatDarkstoreID(n int64) string {
	return fmt.Sprintf("%0*d", darkstoreIDMinWidth, n)
}
