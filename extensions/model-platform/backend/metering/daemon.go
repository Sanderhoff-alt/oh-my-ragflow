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
}

// MeteringDaemon coordinates periodic watermark-driven incremental spend log pulling
// and decoupled periodic daily usage pipeline aggregation.
type MeteringDaemon struct {
	db                  *gorm.DB
	collector           *SpendLogCollector
	pipeline            *UsagePipeline
	pollInterval        time.Duration
	aggregationInterval time.Duration
	overlapWindow       time.Duration
	maxLookback         time.Duration
	lookbackHours       int
	enabled             bool

	stopChan chan struct{}
	stopOnce sync.Once
	stopMu   sync.RWMutex
	started  bool
	stopped  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu                sync.RWMutex
	lastPollTime      time.Time
	lastAggregateTime time.Time
	lastPollErr       error
	lastAggregateErr  error

	totalPolls             int64
	successfulPolls        int64
	failedPolls            int64
	totalAggregations      int64
	successfulAggregations int64
	failedAggregations     int64
}

// NewMeteringDaemon creates a new MeteringDaemon with decoupled poll and aggregation schedules.
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

// Start begins background periodic polling and daily usage aggregation in decoupled worker goroutines.
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
	d.wg.Add(2)
	d.stopMu.Unlock()

	go d.runIngestionWorker(workerCtx)
	go d.runAggregationWorker(workerCtx)
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

	pRes, err := d.pipeline.RunWindow(ctx, lookbackHours)
	if err != nil {
		d.recordAggregateFailure(err)
		return nil, fmt.Errorf("daily usage aggregation failed: %w", err)
	}

	d.recordAggregateSuccess()
	return pRes, nil
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
	}
}
