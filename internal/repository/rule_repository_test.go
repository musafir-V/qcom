package repository

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
)

func TestMarshalRuleItemIncludesPKAndSK(t *testing.T) {
	spec, err := json.Marshal(models.RateModifierSpec{
		DaysOfWeek: []int{5},
		StartTime:  "17:30",
		EndTime:    "23:00",
		Multiplier: 1.2,
		FlatZMW:    0,
	})
	if err != nil {
		t.Fatalf("marshal spec failed: %v", err)
	}

	rule := &models.Rule{
		ID:      "friday_evening",
		Name:    "Friday Evening",
		Family:  models.FamilyRateModifier,
		Enabled: true,
		Priority: 10,
		Version: 1,
		Spec:    spec,
	}

	item, err := marshalRuleItem(rule)
	if err != nil {
		t.Fatalf("marshalRuleItem failed: %v", err)
	}

	pk, ok := item["PK"].(*types.AttributeValueMemberS)
	if !ok || pk.Value != "RULE" {
		t.Fatalf("PK = %#v, want RULE", item["PK"])
	}
	sk, ok := item["SK"].(*types.AttributeValueMemberS)
	if !ok || sk.Value != "rate_modifier#friday_evening#v1" {
		t.Fatalf("SK = %#v, want rate_modifier#friday_evening#v1", item["SK"])
	}

	var got models.Rule
	if err := attributevalue.UnmarshalMap(item, &got); err != nil {
		t.Fatalf("unmarshal map failed: %v", err)
	}
	if got.ID != rule.ID || got.Family != rule.Family || got.Version != rule.Version {
		t.Fatalf("unmarshal mismatch: got %+v, want %+v", got, *rule)
	}
}

func TestLatestRulePrefix(t *testing.T) {
	got := latestRulePrefix(models.FamilyAccumulator, "b2_weekly")
	want := "accumulator#b2_weekly#v"
	if got != want {
		t.Fatalf("latestRulePrefix() = %q, want %q", got, want)
	}
}
