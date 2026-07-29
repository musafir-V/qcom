// internal/repository/cursor.go
package repository

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// encodeStringKeyCursor serializes a DynamoDB LastEvaluatedKey made up solely of
// string attributes into an opaque pagination token, using the given base64
// encoding. Returns "" for an empty/absent key (i.e. the last page). When
// strict is true a non-string attribute is an error; otherwise it is skipped.
func encodeStringKeyCursor(enc *base64.Encoding, lastKey map[string]types.AttributeValue, strict bool) (string, error) {
	if len(lastKey) == 0 {
		return "", nil
	}
	flat := make(map[string]string, len(lastKey))
	for k, v := range lastKey {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			if strict {
				return "", fmt.Errorf("unexpected non-string key attribute %q in cursor", k)
			}
			continue
		}
		flat[k] = s.Value
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		return "", err
	}
	return enc.EncodeToString(raw), nil
}

// decodeStringKeyCursor reverses encodeStringKeyCursor. An empty token yields a
// nil start key (first page).
func decodeStringKeyCursor(enc *base64.Encoding, cursor string) (map[string]types.AttributeValue, error) {
	if strings.TrimSpace(cursor) == "" {
		return nil, nil
	}
	raw, err := enc.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	key := make(map[string]types.AttributeValue, len(flat))
	for k, v := range flat {
		key[k] = &types.AttributeValueMemberS{Value: v}
	}
	return key, nil
}
