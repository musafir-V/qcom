package metrics

import (
	"testing"
	"time"
)

func TestRecordCronStore(t *testing.T) {
	const store = "store-record"

	RecordCronStore(store, 7, 3, 2, 5, 4, 1)

	gauges := map[string]float64{
		"assignment_cron_orders_ready":    7,
		"assignment_cron_eligible_riders": 3,
		"assignment_cron_trips_left_out":  2,
	}
	for name, want := range gauges {
		got, ok := gatherValue(t, name, map[string]string{"store": store})
		if !ok {
			t.Fatalf("%s: no series for store %q", name, store)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}

	counters := map[string]float64{
		"assignment_cron_trips_created_total":         5,
		"assignment_cron_trips_assigned_total":        4,
		"assignment_cron_trips_distance_failed_total": 1,
	}
	for name, want := range counters {
		got, ok := gatherValue(t, name, map[string]string{"store": store})
		if !ok {
			t.Fatalf("%s: no series for store %q", name, store)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestRecordCronStore_GaugesReplaceCountersAccumulate(t *testing.T) {
	const store = "store-accumulate"

	RecordCronStore(store, 10, 0, 0, 2, 1, 0)
	RecordCronStore(store, 4, 0, 0, 3, 1, 0)

	if got, _ := gatherValue(t, "assignment_cron_orders_ready", map[string]string{"store": store}); got != 4 {
		t.Fatalf("orders_ready gauge = %v, want the latest tick's value 4", got)
	}
	if got, _ := gatherValue(t, "assignment_cron_trips_created_total", map[string]string{"store": store}); got != 5 {
		t.Fatalf("trips_created counter = %v, want the sum 5", got)
	}
	if got, _ := gatherValue(t, "assignment_cron_trips_assigned_total", map[string]string{"store": store}); got != 2 {
		t.Fatalf("trips_assigned counter = %v, want the sum 2", got)
	}
}

func TestRecordCronStore_ZeroTickCreatesCounterSeries(t *testing.T) {
	const store = "store-idle"

	RecordCronStore(store, 0, 0, 0, 0, 0, 0)

	if _, ok := gatherValue(t, "assignment_cron_trips_created_total", map[string]string{"store": store}); !ok {
		t.Fatal("expected an idle store to still publish its counter series")
	}
}

func TestSetCronRidersAtPod(t *testing.T) {
	const store = "store-riders"

	SetCronRidersAtPod(store, 6)
	if got, ok := gatherValue(t, "assignment_cron_riders_at_pod", map[string]string{"store": store}); !ok || got != 6 {
		t.Fatalf("riders_at_pod = %v (found=%v), want 6", got, ok)
	}

	// A later tick with a failed on-duty lookup never calls this, so the
	// previous value must survive; an explicit call replaces it.
	SetCronRidersAtPod(store, 2)
	if got, _ := gatherValue(t, "assignment_cron_riders_at_pod", map[string]string{"store": store}); got != 2 {
		t.Fatalf("riders_at_pod = %v, want 2", got)
	}
}

func TestIncCronStoreError(t *testing.T) {
	const store = "store-errors"

	IncCronStoreError(store, "fetch_orders")
	IncCronStoreError(store, "fetch_orders")
	IncCronStoreError(store, "fetch_des")

	got, ok := gatherValue(t, "assignment_cron_store_errors_total", map[string]string{"store": store, "stage": "fetch_orders"})
	if !ok || got != 2 {
		t.Fatalf("fetch_orders errors = %v (found=%v), want 2", got, ok)
	}
	got, ok = gatherValue(t, "assignment_cron_store_errors_total", map[string]string{"store": store, "stage": "fetch_des"})
	if !ok || got != 1 {
		t.Fatalf("fetch_des errors = %v (found=%v), want 1", got, ok)
	}
}

func TestMarkCronRun(t *testing.T) {
	before := time.Now().Unix()
	MarkCronRun()
	after := time.Now().Unix()

	got, ok := gatherValue(t, "assignment_cron_last_run_timestamp_seconds", nil)
	if !ok {
		t.Fatal("no liveness gauge recorded")
	}
	if int64(got) < before || int64(got) > after {
		t.Fatalf("last run = %v, want a timestamp within [%d, %d]", got, before, after)
	}
}
