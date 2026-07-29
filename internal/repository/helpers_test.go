package repository

import (
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
)

func TestEncodeDecodeCursor_RoundTrip(t *testing.T) {
	key := map[string]types.AttributeValue{
		"PK":                 &types.AttributeValueMemberS{Value: "DE#123"},
		"SK":                 &types.AttributeValueMemberS{Value: "METADATA"},
		"assigned_store_key": &types.AttributeValueMemberS{Value: "STORE#1"},
	}

	cursor, err := encodeCursor(key)
	if err != nil {
		t.Fatalf("encodeCursor: %v", err)
	}
	if cursor == "" {
		t.Fatal("expected a non-empty cursor")
	}

	got, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !reflect.DeepEqual(got, key) {
		t.Fatalf("round trip = %#v, want %#v", got, key)
	}
}

func TestEncodeCursor_EmptyKeyMeansLastPage(t *testing.T) {
	for _, key := range []map[string]types.AttributeValue{nil, {}} {
		cursor, err := encodeCursor(key)
		if err != nil {
			t.Fatalf("encodeCursor: %v", err)
		}
		if cursor != "" {
			t.Fatalf("cursor = %q, want empty for an absent last key", cursor)
		}
	}
}

func TestEncodeCursor_RejectsNonStringAttributes(t *testing.T) {
	_, err := encodeCursor(map[string]types.AttributeValue{
		"count": &types.AttributeValueMemberN{Value: "3"},
	})
	if err == nil {
		t.Fatal("expected an error for a non-string key attribute")
	}
}

func TestDecodeCursor(t *testing.T) {
	t.Run("blank means first page", func(t *testing.T) {
		for _, cursor := range []string{"", "   "} {
			got, err := decodeCursor(cursor)
			if err != nil {
				t.Fatalf("decodeCursor(%q): %v", cursor, err)
			}
			if got != nil {
				t.Fatalf("decodeCursor(%q) = %#v, want nil", cursor, got)
			}
		}
	})

	t.Run("rejects malformed tokens", func(t *testing.T) {
		cases := map[string]string{
			"not base64": "!!!not-base64!!!",
			"not json":   base64.URLEncoding.EncodeToString([]byte("not json")),
		}
		for name, cursor := range cases {
			if _, err := decodeCursor(cursor); err == nil {
				t.Fatalf("%s: expected an error", name)
			}
		}
	})
}

func TestEncodeDecodeDisputeCursor_RoundTrip(t *testing.T) {
	key := map[string]types.AttributeValue{
		"PK":                 &types.AttributeValueMemberS{Value: "DISPUTE#9"},
		"dispute_status_key": &types.AttributeValueMemberS{Value: "OPEN"},
	}

	got, err := decodeDisputeCursor(encodeDisputeCursor(key))
	if err != nil {
		t.Fatalf("decodeDisputeCursor: %v", err)
	}
	if !reflect.DeepEqual(got, key) {
		t.Fatalf("round trip = %#v, want %#v", got, key)
	}
}

func TestEncodeDisputeCursor_SkipsNonStringAndEmpty(t *testing.T) {
	if got := encodeDisputeCursor(nil); got != "" {
		t.Fatalf("cursor = %q, want empty for an absent last key", got)
	}

	cursor := encodeDisputeCursor(map[string]types.AttributeValue{
		"PK":    &types.AttributeValueMemberS{Value: "DISPUTE#9"},
		"count": &types.AttributeValueMemberN{Value: "3"},
	})
	got, err := decodeDisputeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeDisputeCursor: %v", err)
	}
	if len(got) != 1 || got["PK"].(*types.AttributeValueMemberS).Value != "DISPUTE#9" {
		t.Fatalf("decoded = %#v, want only the string attribute", got)
	}
}

func TestDecodeDisputeCursor(t *testing.T) {
	got, err := decodeDisputeCursor("")
	if err != nil || got != nil {
		t.Fatalf("decodeDisputeCursor(\"\") = %#v, %v; want nil, nil", got, err)
	}

	for name, cursor := range map[string]string{
		"not base64": "!!!",
		"not json":   base64.StdEncoding.EncodeToString([]byte("not json")),
	} {
		if _, err := decodeDisputeCursor(cursor); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestConvertAttributeValue(t *testing.T) {
	cases := []struct {
		name string
		in   types.AttributeValue
		want interface{}
	}{
		{"string", &types.AttributeValueMemberS{Value: "hi"}, "hi"},
		{"integer", &types.AttributeValueMemberN{Value: "42"}, int64(42)},
		{"float", &types.AttributeValueMemberN{Value: "4.5"}, 4.5},
		{"unparsable integer", &types.AttributeValueMemberN{Value: "9223372036854775808"}, "9223372036854775808"},
		{"unparsable float", &types.AttributeValueMemberN{Value: "1.2.3"}, "1.2.3"},
		{"bool", &types.AttributeValueMemberBOOL{Value: true}, true},
		{"null", &types.AttributeValueMemberNULL{Value: true}, nil},
		{"unsupported type", &types.AttributeValueMemberSS{Value: []string{"a"}}, nil},
		{
			"nested map and list",
			&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"title": &types.AttributeValueMemberS{Value: "Home"},
				"items": &types.AttributeValueMemberL{Value: []types.AttributeValue{
					&types.AttributeValueMemberN{Value: "1"},
					&types.AttributeValueMemberBOOL{Value: false},
				}},
			}},
			map[string]interface{}{
				"title": "Home",
				"items": []interface{}{int64(1), false},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := convertAttributeValue(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("convertAttributeValue = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMarshalAdminUserItem(t *testing.T) {
	user := &models.AdminUser{Username: "ops1", Name: "Ops One", PasswordHash: "hash"}

	item, err := marshalAdminUserItem(user)
	if err != nil {
		t.Fatalf("marshalAdminUserItem: %v", err)
	}

	want := map[string]string{
		"PK":            "ADMIN_USER",
		"SK":            "ops1",
		"username":      "ops1",
		"name":          "Ops One",
		"password_hash": "hash",
	}
	for attr, value := range want {
		s, ok := item[attr].(*types.AttributeValueMemberS)
		if !ok {
			t.Fatalf("attribute %q = %#v, want a string attribute", attr, item[attr])
		}
		if s.Value != value {
			t.Fatalf("attribute %q = %q, want %q", attr, s.Value, value)
		}
	}
}
