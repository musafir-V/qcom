package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Assignment-cron metrics. All are labelled by darkstore ("store") except the
// liveness gauge. The cron runs under a distributed lock, so per-tick values
// are written by whichever instance held the lock that tick.
//
// Dashboard aggregation:
//   - gauges  -> max by (store) (...)                 (freshest value across instances)
//   - counters -> sum by (store) (rate(...[5m]))      (throughput, instance-agnostic)
var (
	cronOrdersReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "assignment_cron_orders_ready",
		Help: "READY_FOR_DELIVERY orders returned by the order service for this store in the latest cron tick.",
	}, []string{"store"})

	cronRidersAtPod = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "assignment_cron_riders_at_pod",
		Help: "On-duty riders present at this store in the latest cron tick.",
	}, []string{"store"})

	cronEligibleRiders = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "assignment_cron_eligible_riders",
		Help: "Eligible (FIFO-assignable) riders for this store in the latest cron tick.",
	}, []string{"store"})

	cronTripsLeftOut = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "assignment_cron_trips_left_out",
		Help: "Unassigned trips remaining for this store after the latest cron tick's assign loop.",
	}, []string{"store"})

	cronTripsCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assignment_cron_trips_created_total",
		Help: "Assignable trips created by the cron for this store.",
	}, []string{"store"})

	cronTripsAssigned = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assignment_cron_trips_assigned_total",
		Help: "Trips successfully assigned to a rider by the cron for this store.",
	}, []string{"store"})

	cronTripsDistanceFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assignment_cron_trips_distance_failed_total",
		Help: "Orders persisted as distance_failed (no drivable route) for this store.",
	}, []string{"store"})

	cronStoreErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assignment_cron_store_errors_total",
		Help: "Cron per-store early-return failures, by stage.",
	}, []string{"store", "stage"})

	cronLastRun = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "assignment_cron_last_run_timestamp_seconds",
		Help: "Unix timestamp when the assignment cron last completed a tick (liveness).",
	})
)

func init() {
	registry.MustRegister(
		cronOrdersReady,
		cronRidersAtPod,
		cronEligibleRiders,
		cronTripsLeftOut,
		cronTripsCreated,
		cronTripsAssigned,
		cronTripsDistanceFailed,
		cronStoreErrors,
		cronLastRun,
	)
}

// RecordCronStore records the per-store snapshot gauges and increments the
// per-store event counters for one processed store in a cron tick. Counters are
// always advanced (by 0 when nothing happened) so every active store's series
// exists and shows up on the dashboard.
func RecordCronStore(store string, ordersReady, eligibleRiders, tripsLeftOut, tripsCreated, tripsAssigned, distanceFailed int) {
	cronOrdersReady.WithLabelValues(store).Set(float64(ordersReady))
	cronEligibleRiders.WithLabelValues(store).Set(float64(eligibleRiders))
	cronTripsLeftOut.WithLabelValues(store).Set(float64(tripsLeftOut))
	cronTripsCreated.WithLabelValues(store).Add(float64(tripsCreated))
	cronTripsAssigned.WithLabelValues(store).Add(float64(tripsAssigned))
	cronTripsDistanceFailed.WithLabelValues(store).Add(float64(distanceFailed))
}

// SetCronRidersAtPod sets the on-duty (present) rider gauge for a store. Kept
// separate because it is best-effort: if the on-duty lookup fails we simply
// leave the previous value rather than reporting a false zero.
func SetCronRidersAtPod(store string, ridersAtPod int) {
	cronRidersAtPod.WithLabelValues(store).Set(float64(ridersAtPod))
}

// IncCronStoreError increments the per-store error counter for a failed stage
// (e.g. "fetch_orders", "fetch_des", "fetch_onduty").
func IncCronStoreError(store, stage string) {
	cronStoreErrors.WithLabelValues(store, stage).Inc()
}

// MarkCronRun stamps the cron liveness gauge with the current time. Call once
// per completed tick.
func MarkCronRun() {
	cronLastRun.Set(float64(time.Now().Unix()))
}
