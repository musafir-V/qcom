package repository

import (
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func strconvFormat(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestIsVonageJWTCacheExpired(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	nowUnix := now.Unix()

	tests := []struct {
		name string
		item map[string]types.AttributeValue
		want bool
	}{
		{
			name: "no ttl attribute",
			item: map[string]types.AttributeValue{
				"Token": &types.AttributeValueMemberS{Value: "jwt"},
			},
			want: false,
		},
		{
			name: "ttl in future",
			item: map[string]types.AttributeValue{
				"TTL": &types.AttributeValueMemberN{Value: strconvFormat(nowUnix + 60)},
			},
			want: false,
		},
		{
			name: "ttl in past",
			item: map[string]types.AttributeValue{
				"TTL": &types.AttributeValueMemberN{Value: strconvFormat(nowUnix - 60)},
			},
			want: true,
		},
		{
			name: "ttl exactly now",
			item: map[string]types.AttributeValue{
				"TTL": &types.AttributeValueMemberN{Value: strconvFormat(nowUnix)},
			},
			want: true,
		},
		{
			name: "invalid ttl",
			item: map[string]types.AttributeValue{
				"TTL": &types.AttributeValueMemberN{Value: "not-a-number"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVonageJWTCacheExpired(tt.item, now); got != tt.want {
				t.Fatalf("isVonageJWTCacheExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
