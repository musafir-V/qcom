package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const (
	punctualityDayPrefix   = "PUNCTUALITY_DAY#"
	punctualityClearPrefix = "PUNCTUALITY_CLEAR#"
)

// PunctualityStoredDay is one frozen arrival score.
type PunctualityStoredDay struct {
	Date          string
	ShiftStart    string
	FirstScan     string
	SignedMinutes int
	ScoredAt      time.Time
}

// PunctualityStoredClear is one settlement of the open aggregate.
type PunctualityStoredClear struct {
	At          time.Time
	Total       int
	Note        string
	Admin       string
	Partial     bool
	OpenMinutes int
}

// PunctualityStoredStart is one saved arrival time.
type PunctualityStoredStart struct {
	Time  string
	SetAt time.Time
}

// PunctualityState is everything stored for one driver's arrival score.
type PunctualityState struct {
	ShiftStart        string
	ShiftStartSetAt   time.Time
	Starts            []PunctualityStoredStart
	OpenMinutes       int
	LastScoredDate    string
	LastScoredMinutes int
	Days              []PunctualityStoredDay
	Settlements       []PunctualityStoredClear
}

// PunctualityRepository stores shift start, scored days, and clears under the
// driver's partition.
type PunctualityRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewPunctualityRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *PunctualityRepository {
	return &PunctualityRepository{client: client, tableName: tableName, logger: logger}
}

type shiftStartJSON struct {
	Time  string `json:"time"`
	SetAt string `json:"set_at"`
}

func decodeShiftStarts(raw, single, singleSetAt string) ([]PunctualityStoredStart, error) {
	if strings.TrimSpace(raw) != "" {
		var rows []shiftStartJSON
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, fmt.Errorf("bad shift_starts: %w", err)
		}
		out := make([]PunctualityStoredStart, 0, len(rows))
		for _, row := range rows {
			setAt, err := parseStoredTime(row.SetAt)
			if err != nil {
				return nil, fmt.Errorf("bad shift start set_at: %w", err)
			}
			out = append(out, PunctualityStoredStart{Time: row.Time, SetAt: setAt})
		}
		return out, nil
	}
	if strings.TrimSpace(single) == "" {
		return nil, nil
	}
	setAt := time.Time{}
	if singleSetAt != "" {
		parsed, err := parseStoredTime(singleSetAt)
		if err != nil {
			return nil, fmt.Errorf("bad shift_start_set_at: %w", err)
		}
		setAt = parsed
	}
	return []PunctualityStoredStart{{Time: single, SetAt: setAt}}, nil
}

func encodeShiftStarts(starts []PunctualityStoredStart) (string, error) {
	rows := make([]shiftStartJSON, len(starts))
	for i, start := range starts {
		rows[i] = shiftStartJSON{Time: start.Time, SetAt: start.SetAt.UTC().Format(time.RFC3339Nano)}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func punctualityDaySK(date, shiftStart string) string {
	return punctualityDayPrefix + date + "#" + shiftStart
}

func punctualityClearSK(at time.Time) string {
	return punctualityClearPrefix + at.UTC().Format(time.RFC3339Nano)
}

func deMetadataKey(phone string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
		"SK": &types.AttributeValueMemberS{Value: "METADATA"},
	}
}

func (r *PunctualityRepository) Load(ctx context.Context, phone string) (PunctualityState, error) {
	op := logging.Start(ctx, r.logger, "PunctualityRepository.Load", logrus.Fields{"phone": phone})
	defer op.End()

	state := PunctualityState{}
	got, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key:       deMetadataKey(phone),
	})
	if err != nil {
		return state, op.Fail(fmt.Errorf("failed to load driver punctuality: %w", err))
	}
	if got.Item != nil {
		var attrs struct {
			ShiftStarts            string `dynamodbav:"shift_starts"`
			ShiftStartTime         string `dynamodbav:"shift_start_time"`
			ShiftStartSetAt        string `dynamodbav:"shift_start_set_at"`
			OpenPunctualityMinutes int    `dynamodbav:"open_punctuality_minutes"`
			LastScoredDate         string `dynamodbav:"last_scored_date"`
			LastScoredMinutes      int    `dynamodbav:"last_scored_minutes"`
		}
		if err := attributevalue.UnmarshalMap(got.Item, &attrs); err != nil {
			return state, op.Fail(fmt.Errorf("failed to unmarshal punctuality attrs: %w", err))
		}
		starts, err := decodeShiftStarts(attrs.ShiftStarts, attrs.ShiftStartTime, attrs.ShiftStartSetAt)
		if err != nil {
			return state, op.Fail(err)
		}
		state.Starts = starts
		state.OpenMinutes = attrs.OpenPunctualityMinutes
		state.LastScoredDate = attrs.LastScoredDate
		state.LastScoredMinutes = attrs.LastScoredMinutes
		if len(starts) > 0 {
			state.ShiftStart = starts[0].Time
			state.ShiftStartSetAt = starts[0].SetAt
		}
	}

	var startKey map[string]types.AttributeValue
	for {
		page, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":     &types.AttributeValueMemberS{Value: "DE!" + phone},
				":prefix": &types.AttributeValueMemberS{Value: "PUNCTUALITY_"},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return state, op.Fail(fmt.Errorf("failed to query punctuality items: %w", err))
		}
		for _, item := range page.Items {
			if err := appendPunctualityItem(&state, item); err != nil {
				return state, op.Fail(err)
			}
		}
		if len(page.LastEvaluatedKey) == 0 {
			break
		}
		startKey = page.LastEvaluatedKey
	}
	return state, nil
}

func appendPunctualityItem(state *PunctualityState, item map[string]types.AttributeValue) error {
	skAttr, ok := item["SK"].(*types.AttributeValueMemberS)
	if !ok {
		return nil
	}
	sk := skAttr.Value
	switch {
	case len(sk) >= len(punctualityDayPrefix) && sk[:len(punctualityDayPrefix)] == punctualityDayPrefix:
		var row struct {
			Date          string `dynamodbav:"date"`
			ShiftStart    string `dynamodbav:"shift_start"`
			FirstScan     string `dynamodbav:"first_scan"`
			SignedMinutes int    `dynamodbav:"signed_minutes"`
			ScoredAt      string `dynamodbav:"scored_at"`
		}
		if err := attributevalue.UnmarshalMap(item, &row); err != nil {
			return fmt.Errorf("unmarshal punctuality day: %w", err)
		}
		scoredAt, err := parseStoredTime(row.ScoredAt)
		if err != nil {
			return fmt.Errorf("bad scored_at: %w", err)
		}
		state.Days = append(state.Days, PunctualityStoredDay{
			Date: row.Date, ShiftStart: row.ShiftStart, FirstScan: row.FirstScan,
			SignedMinutes: row.SignedMinutes, ScoredAt: scoredAt,
		})
	case len(sk) >= len(punctualityClearPrefix) && sk[:len(punctualityClearPrefix)] == punctualityClearPrefix:
		var row struct {
			Total     int    `dynamodbav:"total"`
			Note      string `dynamodbav:"note"`
			Admin     string `dynamodbav:"admin"`
			ClearedAt string `dynamodbav:"cleared_at"`
			Partial   bool   `dynamodbav:"partial"`
		}
		if err := attributevalue.UnmarshalMap(item, &row); err != nil {
			return fmt.Errorf("unmarshal punctuality clear: %w", err)
		}
		at, err := parseStoredTime(row.ClearedAt)
		if err != nil {
			return fmt.Errorf("bad cleared_at: %w", err)
		}
		state.Settlements = append(state.Settlements, PunctualityStoredClear{
			At: at, Total: row.Total, Note: row.Note, Admin: row.Admin, Partial: row.Partial,
		})
	}
	return nil
}

func parseStoredTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func (r *PunctualityRepository) UpdateShiftStarts(ctx context.Context, phone string, starts []PunctualityStoredStart) error {
	op := logging.Start(ctx, r.logger, "PunctualityRepository.UpdateShiftStarts", logrus.Fields{"phone": phone})
	defer op.End()
	raw, err := encodeShiftStarts(starts)
	if err != nil {
		return op.Fail(err)
	}
	earliest, earliestSet := "", ""
	if len(starts) > 0 {
		earliest = starts[0].Time
		earliestSet = starts[0].SetAt.UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(r.tableName),
		Key:                 deMetadataKey(phone),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		UpdateExpression:    aws.String("SET shift_starts = :list, shift_start_time = :t, shift_start_set_at = :set, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":list": &types.AttributeValueMemberS{Value: raw},
			":t":    &types.AttributeValueMemberS{Value: earliest},
			":set":  &types.AttributeValueMemberS{Value: earliestSet},
			":now":  &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("not_found", ErrDENotFound)
		}
		return op.Fail(fmt.Errorf("failed to save shift start: %w", err))
	}
	return nil
}

func (r *PunctualityRepository) PutDay(ctx context.Context, phone string, day PunctualityStoredDay, openMinutes int, lastDate string, lastMinutes int) error {
	op := logging.Start(ctx, r.logger, "PunctualityRepository.PutDay", logrus.Fields{"phone": phone, "date": day.Date})
	defer op.End()
	scoredAt := day.ScoredAt.UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{
				TableName:           aws.String(r.tableName),
				ConditionExpression: aws.String("attribute_not_exists(SK)"),
				Item: map[string]types.AttributeValue{
					"PK":             &types.AttributeValueMemberS{Value: "DE!" + phone},
					"SK":             &types.AttributeValueMemberS{Value: punctualityDaySK(day.Date, day.ShiftStart)},
					"date":           &types.AttributeValueMemberS{Value: day.Date},
					"shift_start":    &types.AttributeValueMemberS{Value: day.ShiftStart},
					"first_scan":     &types.AttributeValueMemberS{Value: day.FirstScan},
					"signed_minutes": &types.AttributeValueMemberN{Value: strconv.Itoa(day.SignedMinutes)},
					"scored_at":      &types.AttributeValueMemberS{Value: scoredAt},
				},
			}},
			{Update: &types.Update{
				TableName:        aws.String(r.tableName),
				Key:              deMetadataKey(phone),
				UpdateExpression: aws.String("SET open_punctuality_minutes = :open, last_scored_date = :date, last_scored_minutes = :mins, updated_at = :now"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":open": &types.AttributeValueMemberN{Value: strconv.Itoa(openMinutes)},
					":date": &types.AttributeValueMemberS{Value: lastDate},
					":mins": &types.AttributeValueMemberN{Value: strconv.Itoa(lastMinutes)},
					":now":  &types.AttributeValueMemberS{Value: now},
				},
			}},
		},
	})
	if err != nil {
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) {
			for _, reason := range canceled.CancellationReasons {
				if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
					return nil
				}
			}
		}
		return op.Fail(fmt.Errorf("failed to save punctuality day: %w", err))
	}
	return nil
}

func (r *PunctualityRepository) PutClear(ctx context.Context, phone string, clear PunctualityStoredClear) error {
	op := logging.Start(ctx, r.logger, "PunctualityRepository.PutClear", logrus.Fields{"phone": phone})
	defer op.End()
	at := clear.At.UTC().Format(time.RFC3339Nano)
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{
				TableName: aws.String(r.tableName),
				Item: map[string]types.AttributeValue{
					"PK":         &types.AttributeValueMemberS{Value: "DE!" + phone},
					"SK":         &types.AttributeValueMemberS{Value: punctualityClearSK(clear.At)},
					"total":      &types.AttributeValueMemberN{Value: strconv.Itoa(clear.Total)},
					"note":       &types.AttributeValueMemberS{Value: clear.Note},
					"admin":      &types.AttributeValueMemberS{Value: clear.Admin},
					"cleared_at": &types.AttributeValueMemberS{Value: at},
					"partial":    &types.AttributeValueMemberBOOL{Value: clear.Partial},
				},
				ConditionExpression: aws.String("attribute_not_exists(SK)"),
			}},
			{Update: &types.Update{
				TableName:        aws.String(r.tableName),
				Key:              deMetadataKey(phone),
				UpdateExpression: aws.String("SET open_punctuality_minutes = :open, updated_at = :now"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":open": &types.AttributeValueMemberN{Value: strconv.Itoa(clear.OpenMinutes)},
					":now":  &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
				},
			}},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to clear punctuality: %w", err))
	}
	return nil
}
