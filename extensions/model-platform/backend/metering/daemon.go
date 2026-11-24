package metering

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// DaemonOption configures the MeteringDaemon.
type DaemonOption func(*MeteringDaemon)

// WithPollInterval sets the interval for incremental spend log collection from gateway (default 5m).
func WithPollInterval(interval time.Duration) DaemonOption {
	return func(d *MeteringDaemon) {
		if interval > 0 {
			d.pollInterval = interval
		}
	}
}

// WithAggregationInterval sets the interval for local daily usage aggregation (default 1h).
func WithAggregationInterval(interval time.Duration) DaemonOption {
	return func(d *MeteringDaemon) {
		if interval > 0 {
			d.aggregationInterval = interval
		}
	}
}

// WithOverlapWindow sets the safety overlap window to prevent edge dropping during incremental pulls (METERING_FETCH_OVERLAP).
func WithOverlapWindow(overlap time.Duration) DaemonOption {
	return func(d *MeteringDaemon) {
		if overlap > 0 {
			d.overlapWindow = overlap
		}
	}
}

// WithMaxLookback sets the maximum historical span allowed for cold-start or catch-up pulls.
func WithMaxLookback(max time.Duration) DaemonOption {
	return func(d *MeteringDaemon) {
		if max > 0 {
			d.maxLookback = max
		}
	}
}

// WithLookbackHours sets the rolling lookback window in hours for daily aggregation (default 48h).
func WithLookbackHours(hours int) DaemonOption {
	return func(d *MeteringDaemon) {
		if hours > 0 {
			d.lookbackHours = hours
		}
	}
}

// WithDaemonEnabled enables or disables the daemon runner (useful for multi-instance role sharding).
func WithDaemonEnabled(enabled bool) DaemonOption {
	return func(d *MeteringDaemon) {
		d.enabled = enabled
	}
}

// WithDaemonTimezone sets the business billing timezone for daily usage aggregation.
func WithDaemonTimezone(loc *time.Location) DaemonOption {
	return func(d *MeteringDaemon) {
		if loc != nil && d.pipeline != nil {
			d.pipeline.SetLocation(loc)
		}
	}
}

// WithRetentionDays configures the raw spend log retention window in days (METERING_SPEND_LOG_RETENTION_DAYS).
// Values <= 0 disable automated purging so raw spend logs are retained indefinitely (default 0).
func WithRetentionDays(days int) DaemonOption {
	return func(d *MeteringDaemon) {
		d.retentionDays = days
	}
}

// DaemonMetrics captures observability metrics for the daemon.
type DaemonMetrics struct {
	TotalPolls             int64     `json:"total_polls"`
	SuccessfulPolls        int64     `json:"successful_polls"`
	FailedPolls            int64     `json:"failed_polls"`
	LastPollTime           time.Time `json:"last_poll_time"`
	LastPollError          string    `json:"last_poll_error,omitempty"`
	TotalAggregations      int64     `json:"total_aggregations"`
	SuccessfulAggregations int64     `json:"successful_aggregations"`
	FailedAggregations     int64     `json:"failed_aggregations"`
	LastAggregateTime      time.Time `json:"last_aggregate_time"`
	LastAggregateError     string    `json:"last_aggregate_error,omitempty"`
	TotalPurges            int64     `json:"total_purges"`
	SuccessfulPurges       int64     `json:"successful_purges"`
	FailedPurges           int64     `json:"failed_purges"`
	TotalPurgedRows        int64     `json:"total_purged_rows"`
	LastPurgeTime          time.Time `json:"last_purge_time"`
	LastPurgeError         string    `json:"last_purge_error,omitempty"`
}

// MeteringDaemon coordinates periodic watermark-driven incremental spend log pulling,
// decoupled periodic daily usage pipeline aggregation, and opt-in raw spend log retention sweeps.
type MeteringDaemon struct {
	db                  *gorm.DB
	collector           *SpendLogCollector
	pipeline            *UsagePipeline
	pollInterval        time.Duration
	aggregationInterval time.Duration
	overlapWindow       time.Duration
	maxLookback         time.Duration
	lookbackHours       int
	retentionDays       int
	purgeInterval       time.Duration
	enabled             bool

	stopChan chan struct{}
	stopOnce sync.Once
	stopMu   sync.RWMutex
	started  bool
	stopped  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// pipelineMu serializes daily usage aggregation and raw spend log purging within this daemon
	// so a purge sweep cannot delete rows out from under an in-flight aggregation across midnight.
	pipelineMu sync.Mutex

	mu                sync.RWMutex
	lastPollTime      time.Time
	lastAggregateTime time.Time
	lastPurgeTime     time.Time
	lastPollErr       error
	lastAggregateErr  error
	lastPurgeErr      error

	totalPolls             int64
	successfulPolls        int64
	failedPolls            int64
	totalAggregations      int64
	successfulAggregations int64
	failedAggregations     int64
	totalPurges            int64
	successfulPurges       int64
	failedPurges           int64
	totalPurgedRows        int64
}

// NewMeteringDaemon creates a new MeteringDaemon with decoupled poll, aggregation, and optional retention schedules.
// Environment variables (METERING_*) serve as defaults, overridden by explicit DaemonOptions.
func NewMeteringDaemon(db *gorm.DB, collector *SpendLogCollector, pipeline *UsagePipeline, opts ...DaemonOption) *MeteringDaemon {
	d := &MeteringDaemon{
		db:                  db,
		collector:           collector,
		pipeline:            pipeline,
		pollInterval:        5 * time.Minute,
		aggregationInterval: 1 * time.Hour,
		overlapWindow:       5 * time.Minute,
		maxLookback:         30 * 24 * time.Hour,
		lookbackHours:       48,
		retentionDays:       0,
		purgeInterval:       24 * time.Hour,
		enabled:             true,
		stopChan:            make(chan struct{}),
	}

	// Load environment variable defaults if set
	if v := os.Getenv("METERING_POLL_INTERVAL"); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			d.pollInterval = dur
		}
	}
	if v := os.Getenv("METERING_AGGREGATION_INTERVAL"); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			d.aggregationInterval = dur
		}
	}
	if v := os.Getenv("METERING_FETCH_OVERLAP"); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			d.overlapWindow = dur
		}
	}
	if v := os.Getenv("METERING_FETCH_MAX_LOOKBACK"); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			d.maxLookback = dur
		}
	}
	if v := os.Getenv("METERING_AGGREGATION_LOOKBACK_HOURS"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			d.lookbackHours = hours
		}
	}
	if v := os.Getenv("METERING_SPEND_LOG_RETENTION_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil {
			d.retentionDays = days
		}
	}
	if v := os.Getenv("METERING_DAEMON_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			d.enabled = b
		}
	}

	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Start begins background periodic polling, daily usage aggregation, and (when retentionDays > 0)
// periodic raw spend log retention sweeps in decoupled worker goroutines.
func (d *MeteringDaemon) Start(ctx context.Context) {
	if !d.enabled {
		return
	}

	d.stopMu.Lock()
	if d.stopped || d.started {
		d.stopMu.Unlock()
		return
	}
	d.started = true
	workerCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	runPurge := d.retentionDays > 0
	if runPurge {
		d.wg.Add(3)
	} else {
		d.wg.Add(2)
	}
	d.stopMu.Unlock()

	go d.runIngestionWorker(workerCtx)
	go d.runAggregationWorker(workerCtx)
	if runPurge {
		go d.runPurgeWorker(workerCtx)
	}
}

func (d *MeteringDaemon) runIngestionWorker(ctx context.Context) {
	defer d.wg.Done()
	pollTicker := time.NewTicker(d.pollInterval)
	defer pollTicker.Stop()

	// Run initial execution on startup
	_, _ = d.PollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopChan:
			return
		case <-pollTicker.C:
			_, _ = d.PollOnce(ctx)
		}
	}
}

func (d *MeteringDaemon) runAggregationWorker(ctx context.Context) {
	defer d.wg.Done()
	aggTicker := time.NewTicker(d.aggregationInterval)
	defer aggTicker.Stop()

	// Run initial execution on startup
	_, _ = d.AggregateOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopChan:
			return
		case <-aggTicker.C:
			_, _ = d.AggregateOnce(ctx)
		}
	}
}

func (d *MeteringDaemon) runPurgeWorker(ctx context.Context) {
	defer d.wg.Done()
	purgeTicker := time.NewTicker(d.purgeInterval)
	defer purgeTicker.Stop()

	// Run initial execution on startup so short-lived processes still honor retention policy.
	_, _ = d.PurgeOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopChan:
			return
		case <-purgeTicker.C:
			_, _ = d.PurgeOnce(ctx)
		}
	}
}

// Stop gracefully terminates the daemon background loops and cancels in-flight operations.
func (d *MeteringDaemon) Stop() {
	d.stopOnce.Do(func() {
		d.stopMu.Lock()
		d.stopped = true
		cancel := d.cancel
		d.stopMu.Unlock()

		if cancel != nil {
			cancel()
		}
		close(d.stopChan)
		d.wg.Wait()
	})
}

// PollOnce executes a single cycle of incremental spend log collection from gateway.
func (d *MeteringDaemon) PollOnce(ctx context.Context) (*CollectResult, error) {
	atomic.AddInt64(&d.totalPolls, 1)
	now := time.Now().UTC()

	d.mu.Lock()
	d.lastPollTime = now
	d.mu.Unlock()

	if d.collector == nil {
		err := errors.New("collector handle cannot be nil")
		d.recordPollFailure(err)
		return nil, err
	}

	cRes, err := d.collector.CollectIncremental(ctx, d.overlapWindow, d.maxLookback)
	if err != nil {
		d.recordPollFailure(err)
		return nil, fmt.Errorf("incremental spend log collection failed: %w", err)
	}

	d.recordPollSuccess()
	return cRes, nil
}

// AggregateOnce executes a single cycle of daily usage aggregation.
func (d *MeteringDaemon) AggregateOnce(ctx context.Context) (*PipelineResult, error) {
	atomic.AddInt64(&d.totalAggregations, 1)
	now := time.Now().UTC()

	d.mu.Lock()
	d.lastAggregateTime = now
	d.mu.Unlock()

	if d.pipeline == nil {
		err := errors.New("pipeline handle cannot be nil")
		d.recordAggregateFailure(err)
		return nil, err
	}

	lookbackHours := d.lookbackHours
	if lookbackHours <= 0 {
		lookbackHours = 48
	}

	d.pipelineMu.Lock()
	pRes, err := d.pipeline.RunWindow(ctx, lookbackHours)
	d.pipelineMu.Unlock()
	if err != nil {
		d.recordAggregateFailure(err)
		return nil, fmt.Errorf("daily usage aggregation failed: %w", err)
	}

	d.recordAggregateSuccess()
	return pRes, nil
}

// PurgeOnce executes a single cycle of raw spend log retention cleanup.
// When retentionDays <= 0, automated purging is disabled and it returns a skipped result with nil error.
// To protect the financial ledger from partial-day overwrites during rolling recalculations,
// purging is serialized with AggregateOnce and the effective cutoff never advances past the
// day-aligned start of the aggregation lookback window.
func (d *MeteringDaemon) PurgeOnce(ctx context.Context) (*PurgeResult, error) {
	if d.retentionDays <= 0 {
		return &PurgeResult{Skipped: true}, nil
	}

	atomic.AddInt64(&d.totalPurges, 1)
	now := time.Now().UTC()

	d.mu.Lock()
	d.lastPurgeTime = now
	d.mu.Unlock()

	if d.pipeline == nil {
		err := errors.New("pipeline handle cannot be nil")
		d.recordPurgeFailure(err)
		return nil, err
	}

	d.pipelineMu.Lock()
	cutoff := d.computeSafePurgeCutoff(time.Now().UTC())
	res, err := d.pipeline.PurgeExpiredSpendLogs(ctx, cutoff, DefaultPurgeBatchSize)
	d.pipelineMu.Unlock()

	if res != nil && res.DeletedCount > 0 {
		atomic.AddInt64(&d.totalPurgedRows, res.DeletedCount)
	}
	if err != nil {
		d.recordPurgeFailure(err)
		return res, fmt.Errorf("spend log retention purge failed: %w", err)
	}

	d.recordPurgeSuccess()
	return res, nil
}

// computeSafePurgeCutoff computes Now() - retentionDays*24h clamped so it never intrudes into
// the active day-aligned aggregation lookback window (preventing replacement upserts from overwriting
// daily financial totals with truncated raw logs).
func (d *MeteringDaemon) computeSafePurgeCutoff(nowUTC time.Time) time.Time {
	retentionCutoff := nowUTC.Add(-time.Duration(d.retentionDays) * 24 * time.Hour)

	lookbackHours := d.lookbackHours
	if lookbackHours <= 0 {
		lookbackHours = 48
	}

	loc := time.UTC
	if d.pipeline != nil {
		loc = d.pipeline.Location()
	}
	nowInLoc := nowUTC.In(loc)
	rawAggStart := nowInLoc.Add(-time.Duration(lookbackHours) * time.Hour)
	aggWindowStartUTC := time.Date(rawAggStart.Year(), rawAggStart.Month(), rawAggStart.Day(), 0, 0, 0, 0, loc).UTC()

	if retentionCutoff.After(aggWindowStartUTC) {
		return aggWindowStartUTC
	}
	return retentionCutoff
}

// PollAndAggregate executes both log polling and daily usage aggregation in sequence.
func (d *MeteringDaemon) PollAndAggregate(ctx context.Context) (*CollectResult, *PipelineResult, error) {
	cRes, err := d.PollOnce(ctx)
	if err != nil {
		return nil, nil, err
	}
	pRes, err := d.AggregateOnce(ctx)
	if err != nil {
		return cRes, nil, err
	}
	return cRes, pRes, nil
}

func (d *MeteringDaemon) recordPollSuccess() {
	atomic.AddInt64(&d.successfulPolls, 1)
	d.mu.Lock()
	d.lastPollErr = nil
	d.mu.Unlock()
}

func (d *MeteringDaemon) recordPollFailure(err error) {
	atomic.AddInt64(&d.failedPolls, 1)
	d.mu.Lock()
	d.lastPollErr = err
	d.mu.Unlock()
}

func (d *MeteringDaemon) recordAggregateSuccess() {
	atomic.AddInt64(&d.successfulAggregations, 1)
	d.mu.Lock()
	d.lastAggregateErr = nil
	d.mu.Unlock()
}

func (d *MeteringDaemon) recordAggregateFailure(err error) {
	atomic.AddInt64(&d.failedAggregations, 1)
	d.mu.Lock()
	d.lastAggregateErr = err
	d.mu.Unlock()
}

func (d *MeteringDaemon) recordPurgeSuccess() {
	atomic.AddInt64(&d.successfulPurges, 1)
	d.mu.Lock()
	d.lastPurgeErr = nil
	d.mu.Unlock()
}

func (d *MeteringDaemon) recordPurgeFailure(err error) {
	atomic.AddInt64(&d.failedPurges, 1)
	d.mu.Lock()
	d.lastPurgeErr = err
	d.mu.Unlock()
}

// TotalPolls returns the total number of poll attempts.
func (d *MeteringDaemon) TotalPolls() int64 {
	return atomic.LoadInt64(&d.totalPolls)
}

// SuccessfulPolls returns the number of successful poll cycles.
func (d *MeteringDaemon) SuccessfulPolls() int64 {
	return atomic.LoadInt64(&d.successfulPolls)
}

// FailedPolls returns the number of failed poll cycles.
func (d *MeteringDaemon) FailedPolls() int64 {
	return atomic.LoadInt64(&d.failedPolls)
}

// TotalAggregations returns the total number of aggregation runs.
func (d *MeteringDaemon) TotalAggregations() int64 {
	return atomic.LoadInt64(&d.totalAggregations)
}

// SuccessfulAggregations returns the number of successful aggregation runs.
func (d *MeteringDaemon) SuccessfulAggregations() int64 {
	return atomic.LoadInt64(&d.successfulAggregations)
}

// FailedAggregations returns the number of failed aggregation runs.
func (d *MeteringDaemon) FailedAggregations() int64 {
	return atomic.LoadInt64(&d.failedAggregations)
}

// TotalPurges returns the total number of retention purge attempts.
func (d *MeteringDaemon) TotalPurges() int64 {
	return atomic.LoadInt64(&d.totalPurges)
}

// SuccessfulPurges returns the number of successful retention purge runs.
func (d *MeteringDaemon) SuccessfulPurges() int64 {
	return atomic.LoadInt64(&d.successfulPurges)
}

// FailedPurges returns the number of failed retention purge runs.
func (d *MeteringDaemon) FailedPurges() int64 {
	return atomic.LoadInt64(&d.failedPurges)
}

// TotalPurgedRows returns the cumulative number of raw spend log rows removed by retention purges.
func (d *MeteringDaemon) TotalPurgedRows() int64 {
	return atomic.LoadInt64(&d.totalPurgedRows)
}

// LastPollTime returns the timestamp of the most recent poll attempt.
func (d *MeteringDaemon) LastPollTime() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastPollTime
}

// LastAggregateTime returns the timestamp of the most recent aggregation run.
func (d *MeteringDaemon) LastAggregateTime() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastAggregateTime
}

// LastPurgeTime returns the timestamp of the most recent retention purge attempt.
func (d *MeteringDaemon) LastPurgeTime() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastPurgeTime
}

// LastPollError returns the error produced during the most recent poll attempt, if any.
func (d *MeteringDaemon) LastPollError() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastPollErr
}

// LastAggregateError returns the error produced during the most recent aggregation run, if any.
func (d *MeteringDaemon) LastAggregateError() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastAggregateErr
}

// LastPurgeError returns the error produced during the most recent retention purge attempt, if any.
func (d *MeteringDaemon) LastPurgeError() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastPurgeErr
}

// GetMetrics returns a snapshot of daemon observability metrics.
func (d *MeteringDaemon) GetMetrics() DaemonMetrics {
	d.mu.RLock()
	defer d.mu.RUnlock()

	lastPollErrStr := ""
	if d.lastPollErr != nil {
		lastPollErrStr = d.lastPollErr.Error()
	}

	lastAggErrStr := ""
	if d.lastAggregateErr != nil {
		lastAggErrStr = d.lastAggregateErr.Error()
	}

	lastPurgeErrStr := ""
	if d.lastPurgeErr != nil {
		lastPurgeErrStr = d.lastPurgeErr.Error()
	}

	return DaemonMetrics{
		TotalPolls:             atomic.LoadInt64(&d.totalPolls),
		SuccessfulPolls:        atomic.LoadInt64(&d.successfulPolls),
		FailedPolls:            atomic.LoadInt64(&d.failedPolls),
		LastPollTime:           d.lastPollTime,
		LastPollError:          lastPollErrStr,
		TotalAggregations:      atomic.LoadInt64(&d.totalAggregations),
		SuccessfulAggregations: atomic.LoadInt64(&d.successfulAggregations),
		FailedAggregations:     atomic.LoadInt64(&d.failedAggregations),
		LastAggregateTime:      d.lastAggregateTime,
		LastAggregateError:     lastAggErrStr,
		TotalPurges:            atomic.LoadInt64(&d.totalPurges),
		SuccessfulPurges:       atomic.LoadInt64(&d.successfulPurges),
		FailedPurges:           atomic.LoadInt64(&d.failedPurges),
		TotalPurgedRows:        atomic.LoadInt64(&d.totalPurgedRows),
		LastPurgeTime:          d.lastPurgeTime,
		LastPurgeError:         lastPurgeErrStr,
	}
}
