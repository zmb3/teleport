/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package local

import (
	"context"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/lib/utils"
)

var (
	usageEventsSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageEventsSubmitted,
		Help:      "a count of usage events that have been generated",
	})

	usageBatchesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageBatches,
		Help:      "a count of batches enqueued for submission",
	})

	usageEventsRequeuedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageEventsRequeued,
		Help:      "a count of events that were requeued after a submission failed",
	})

	usageBatchSubmissionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageBatchSubmissionDuration,
		Help:      "a histogram of durations it took to submit a batch",
	})

	usageBatchesSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageBatchesSubmitted,
		Help:      "a count of event batches successfully submitted",
	})

	usageBatchesFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageBatchesFailed,
		Help:      "a count of event batches that failed to submit",
	})

	usageEventsDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: teleport.MetricNamespace,
		Name:      teleport.MetricUsageEventsDropped,
		Help:      "a count of events dropped due to submission buffer overflow",
	})

	usagePrometheusCollectors = []prometheus.Collector{
		usageEventsSubmitted, usageBatchesTotal, usageEventsRequeuedTotal,
		usageBatchSubmissionDuration, usageBatchesSubmitted, usageBatchesFailed,
		usageEventsDropped,
	}
)

// DiscardUsageReporter is a dummy usage reporter that drops all events.
type DiscardUsageReporter struct{}

func (d *DiscardUsageReporter) SubmitAnonymizedUsageEvents() error {
	// do nothing
	return nil
}

// NewDiscardUsageReporter creates a new usage reporter that drops all events.
func NewDiscardUsageReporter() *DiscardUsageReporter {
	return &DiscardUsageReporter{}
}

// UsageSubmitFunc is a func that submits a batch of usage events.
type UsageSubmitFunc[T any] func(reporter *UsageReporter[T], batch []*T) error

type UsageReporter[T any] struct {
	// Entry is a log entry
	*log.Entry

	clock clockwork.Clock

	ctx context.Context

	cancel context.CancelFunc

	// anonymizer is the anonymizer used for filtered audit events.
	anonymizer utils.Anonymizer

	// events receives batches incoming events from various Teleport components
	events chan []*T

	// buf stores events for batching
	buf []*T

	// submissionQueue queues events for submission
	submissionQueue chan []*T

	// submit is the func that submits batches of events to a backend
	submit UsageSubmitFunc[T]

	// minBatchSize is the minimum batch size before a submit is triggered due
	// to size.
	minBatchSize int

	// maxBatchSize is the maximum size of a batch that may be sent at once.
	maxBatchSize int

	// maxBatchAge is the
	maxBatchAge time.Duration

	// maxBufferSize is the maximum number of events that can be queued in the
	// buffer.
	maxBufferSize int

	// submitDelay is the amount of delay added between all batch submission
	// attempts.
	submitDelay time.Duration

	// ready is used to indicate the reporter is ready for its clock to be
	// manipulated, used to avoid race conditions in tests.
	ready chan struct{}
}

// runSubmit starts the submission thread. It should be run as a background
// goroutine to ensure AddEventToQueue() never blocks.
func (r *UsageReporter[T]) runSubmit() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case batch := <-r.submissionQueue:
			t0 := time.Now()

			if err := r.submit(r, batch); err != nil {
				r.Warnf("Failed to submit batch of %d usage events: %v", len(batch), err)

				usageBatchesFailed.Inc()

				// Put the failed events back on the queue.
				r.resubmitEvents(batch)
			} else {
				usageBatchesSubmitted.Inc()

				r.Infof("usage reporter successfully submitted batch of %d events", len(batch))
			}

			usageBatchSubmissionDuration.Observe(time.Since(t0).Seconds())
		}

		// Always sleep a bit to avoid spamming the server.
		r.clock.Sleep(r.submitDelay)
	}
}

// enqueueBatch prepares a batch for submission, removing it from the buffer and
// adding it to the submission queue.
func (r *UsageReporter[T]) enqueueBatch() {
	if len(r.buf) == 0 {
		// Nothing to do.
		return
	}

	var events []*T
	var remaining []*T
	if len(r.buf) > r.maxBatchSize {
		// Split the request and send the first batch. Any remaining events will
		// sit in the buffer to send with the next batch.
		events = r.buf[:r.maxBatchSize]
		remaining = r.buf[r.maxBatchSize:]
	} else {
		// The event buf is small enough to send in one request. We'll replace
		// the buf to allow any excess memory from the last buf to be GC'd.
		events = r.buf
		remaining = make([]*T, 0, r.minBatchSize)
	}

	select {
	case r.submissionQueue <- events:
		// Wrote to the queue successfully, so swap buf with the shortened one.
		r.buf = remaining

		usageBatchesTotal.Inc()

		r.Debugf("usage reporter has enqueued batch of %d events", len(events))
	default:
		// The queue is full, we'll try again later. Leave the existing buf in
		// place.
	}
}

// Run begins processing incoming usage events. It should be run in a goroutine.
func (r *UsageReporter[T]) Run() {
	timer := r.clock.NewTimer(r.maxBatchAge)

	// Also start the submission goroutine.
	go r.runSubmit()

	// Mark as ready for testing: `clock.Advance()` has no effect if `timer`
	// hasn't been initialized.
	close(r.ready)

	r.Debug("usage reporter is ready")

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-timer.Chan():
			// Once the timer triggers, send any non-empty batch.
			timer.Reset(r.maxBatchAge)
			r.enqueueBatch()
		case events := <-r.events:
			// If the buffer's already full, just warn and discard.
			if len(r.buf) >= r.maxBufferSize {
				// TODO: What level should we log usage submission errors at?
				r.Warnf("Usage event buffer is full, %d events will be discarded", len(events))

				usageEventsDropped.Add(float64(len(events)))
				break
			}

			if len(r.buf)+len(events) > r.maxBufferSize {
				keep := r.maxBufferSize - len(r.buf)
				r.Warnf("Usage event buffer is full, %d events will be discarded", len(events)-keep)
				events = events[:keep]

				usageEventsDropped.Add(float64(len(events) - keep))
			}

			r.buf = append(r.buf, events...)

			// If we've accumulated enough events to trigger an early send, do
			// so and reset the timer.
			if len(r.buf) >= r.minBatchSize {
				timer.Reset(r.maxBatchAge)
				r.enqueueBatch()
			}
		}
	}
}

func (r *UsageReporter[T]) AddEventToQueue(event func(timestamp *timestamppb.Timestamp) (*T, error)) error {
	timestamp := timestamppb.New(r.clock.Now())
	converted, err := event(timestamp)
	if err != nil {
		return trace.Wrap(err)
	}

	usageEventsSubmitted.Inc()

	r.events <- []*T{converted}

	return nil
}

// resubmitEvents resubmits events that have already been processed (in case of
// some error during submission).
func (r *UsageReporter[T]) resubmitEvents(events []*T) {
	usageEventsRequeuedTotal.Add(float64(len(events)))

	r.events <- events
}

type UsageReporterOptions[T any] struct {
	Ctx context.Context
	//SubmitFunc is a func that submits a batch of usage events.
	SubmitFunc UsageSubmitFunc[T]
	// MinBatchSize determines the size at which a batch is sent
	// regardless of elapsed time.
	MinBatchSize int
	// MaxBatchSize is the largest batch size that will be sent to
	// the server; batches larger than this will be split into multiple
	// requests.
	MaxBatchSize int
	// MaxBatchAge is the maximum age a batch may reach before
	// being flushed, regardless of the batch size
	MaxBatchAge time.Duration
	// MaxBufferSize is the maximum size to which the event buffer
	// may grow. Events submitted once this limit is reached will be discarded.
	// Events that were in the submission queue that fail to submit may also be
	// discarded when requeued.
	MaxBufferSize int
	// SubmitDelay is a mandatory delay added to each batch submission
	// to avoid spamming the prehog instance.
	SubmitDelay time.Duration
}

// NewUsageReporter creates a new usage reporter. `Run()` must be executed to
// process incoming events.
func NewUsageReporter[T any](options *UsageReporterOptions[T]) *UsageReporter[T] {
	l := log.WithFields(log.Fields{
		trace.Component: teleport.Component(teleport.ComponentUsageReporting),
	})

	ctx, cancel := context.WithCancel(options.Ctx)
	return &UsageReporter[T]{
		Entry:           l,
		ctx:             ctx,
		cancel:          cancel,
		events:          make(chan []*T, 1),
		submissionQueue: make(chan []*T, 1),
		submit:          options.SubmitFunc,
		clock:           clockwork.NewRealClock(),
		minBatchSize:    options.MinBatchSize,
		maxBatchSize:    options.MaxBatchSize,
		maxBatchAge:     300 * time.Second,
		maxBufferSize:   options.MaxBufferSize,
		submitDelay:     options.SubmitDelay,
		ready:           make(chan struct{}),
	}
}
