// Command punctuality-backfill scores Zambia days that have a shift start and
// an on-duty store scan, but no score yet. An unused start on a scored day can
// still be filled. Pass -apply to write. Without it, the command only reports
// how many days would be scored.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

func main() {
	apply := flag.Bool("apply", false, "write scores; without this the command only reports")
	onlyPhone := flag.String("phone", "", "limit to one driver phone, including the leading +")
	flag.Parse()

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	table := os.Getenv("DYNAMODB_TABLE_NAME")
	if table == "" {
		table = "QComTable"
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{})
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		logger.WithError(err).Fatal("load aws config")
	}
	client := dynamodb.NewFromConfig(awsCfg)
	punctualityRepo := repository.NewPunctualityRepository(client, table, logger)
	events := repository.NewDEStatusEventRepository(client, table, logger)
	svc := service.NewPunctualityService(service.NewDynamoPunctualityStore(punctualityRepo), timezone.Now, logger)

	var startKey map[string]types.AttributeValue
	drivers := 0
	scored := 0
	for {
		page, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        &table,
			FilterExpression: strPtr("SK = :meta AND attribute_exists(shift_start_time)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":meta": &types.AttributeValueMemberS{Value: "METADATA"},
			},
			ProjectionExpression: strPtr("PK, phone_number, de_id, shift_start_set_at, shift_starts"),
			ExclusiveStartKey:    startKey,
		})
		if err != nil {
			logger.WithError(err).Fatal("scan drivers")
		}
		for _, item := range page.Items {
			phone := attrS(item, "phone_number")
			if phone == "" {
				pk := attrS(item, "PK")
				if len(pk) > 3 && pk[:3] == "DE!" {
					phone = pk[3:]
				}
			}
			if phone == "" || (*onlyPhone != "" && phone != *onlyPhone) {
				continue
			}
			since, ok := earliestSetAt(attrS(item, "shift_starts"), attrS(item, "shift_start_set_at"))
			if !ok {
				continue
			}
			scans, err := events.ListOnDutyScansSince(ctx, phone, since)
			if err != nil {
				logger.WithError(err).WithField("phone", phone).Error("list scans")
				continue
			}
			arrivals := make([]service.ArrivalScan, 0, len(scans))
			for _, scan := range scans {
				at, err := time.Parse(time.RFC3339Nano, scan.TS)
				if err != nil {
					at, err = time.Parse(time.RFC3339, scan.TS)
				}
				if err != nil {
					continue
				}
				arrivals = append(arrivals, service.ArrivalScan{At: at, Reason: string(scan.Reason)})
			}
			drivers++
			if !*apply {
				fmt.Printf("would score %s %s from %d scans since %s\n", attrS(item, "de_id"), phone, len(arrivals), since.UTC().Format(time.RFC3339))
				continue
			}
			n, err := svc.BackfillArrivals(ctx, phone, arrivals)
			if err != nil {
				logger.WithError(err).WithField("phone", phone).Error("backfill")
				continue
			}
			scored += n
			if n > 0 {
				fmt.Printf("scored %d day(s) for %s %s\n", n, attrS(item, "de_id"), phone)
			}
		}
		if len(page.LastEvaluatedKey) == 0 {
			break
		}
		startKey = page.LastEvaluatedKey
	}
	if *apply {
		fmt.Printf("drivers=%d newly_scored_days=%d\n", drivers, scored)
	} else {
		fmt.Printf("drivers=%d dry_run=true\n", drivers)
	}
}

func earliestSetAt(rawList, single string) (time.Time, bool) {
	var earliest time.Time
	if rawList != "" {
		var rows []struct {
			SetAt string `json:"set_at"`
		}
		if err := json.Unmarshal([]byte(rawList), &rows); err == nil {
			for _, row := range rows {
				at, ok := parseStored(row.SetAt)
				if !ok {
					continue
				}
				if earliest.IsZero() || at.Before(earliest) {
					earliest = at
				}
			}
		}
	}
	if earliest.IsZero() {
		at, ok := parseStored(single)
		if !ok {
			return time.Time{}, false
		}
		earliest = at
	}
	return earliest, true
}

func parseStored(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	return t, err == nil
}

func attrS(item map[string]types.AttributeValue, name string) string {
	v, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return v.Value
}

func strPtr(s string) *string { return &s }
