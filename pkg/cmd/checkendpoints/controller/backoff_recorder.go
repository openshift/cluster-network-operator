package controller

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openshift/library-go/pkg/operator/events"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// Recorder is a stripped down version of the library-go events.Recorder interface.
type Recorder interface {
	Event(reason, message string)
	Eventf(reason, messageFmt string, args ...interface{})
	Warning(reason, message string)
	Warningf(reason, messageFmt string, args ...interface{})
}

// NewBackoffEventRecorder returns a new Recorder that keeps track of the rate of events
// being recorded and if the rate is too fast, suspend recording events for a while, keeping
// events in memory. When event recording is resumed, record a single summary events for all
// the events for a given event reason. By default, event recording id paused for 30 minutes
// when the rate of events exceed either 30 events in 30 seconds or 600 events in 10 minutes.
func NewBackoffEventRecorder(recorder events.Recorder, options ...backoffEventRecorderOptionFunc) Recorder {
	m := &backoffEventRecorder{
		recorder:            recorder,
		shortWindow:         30 * time.Second,
		longWindow:          10 * time.Minute,
		backoff:             30 * time.Minute,
		shortWindowCountMax: 30,
		longWindowCountMax:  600,
	}
	m.shortWindowTicker = time.NewTicker(m.shortWindow)
	m.longWindowTicker = time.NewTicker(m.longWindow)
	for _, option := range options {
		option(m)
	}
	return m
}

// backoffEventRecorderOptionFunc customizes backoffEventRecorder
type backoffEventRecorderOptionFunc func(recorder *backoffEventRecorder)

// WithShortWindow sets the short window excessive event rate
func WithShortWindow(duration time.Duration, maxCount int) backoffEventRecorderOptionFunc {
	return func(recorder *backoffEventRecorder) {
		if recorder.shortWindowTicker != nil {
			recorder.shortWindowTicker.Stop()
		}
		recorder.shortWindow = duration
		recorder.shortWindowTicker = time.NewTicker(duration)
		recorder.shortWindowCountMax = maxCount
	}
}

// WithLongWindow sets the long window excessive event rate
func WithLongWindow(duration time.Duration, maxCount int) backoffEventRecorderOptionFunc {
	return func(recorder *backoffEventRecorder) {
		if recorder.longWindowTicker != nil {
			recorder.longWindowTicker.Stop()
		}
		recorder.longWindow = duration
		recorder.longWindowTicker = time.NewTicker(duration)
		recorder.longWindowCountMax = maxCount
	}
}

// WithBackoff sets the duration of the event backoff
func WithBackoff(duration time.Duration) backoffEventRecorderOptionFunc {
	return func(recorder *backoffEventRecorder) {
		if recorder.backoffTicker != nil {
			recorder.backoffTicker.Stop()
		}
		recorder.backoff = duration
		recorder.backoffTicker = nil
	}
}

func (r *backoffEventRecorder) Event(reason, message string) {
	r.event(corev1.EventTypeNormal, reason, message)
}

func (r *backoffEventRecorder) Eventf(reason, messageFmt string, args ...interface{}) {
	r.Event(reason, fmt.Sprintf(messageFmt, args...))
}

func (r *backoffEventRecorder) Warning(reason, message string) {
	r.event(corev1.EventTypeWarning, reason, message)
}

func (r *backoffEventRecorder) Warningf(reason, messageFmt string, args ...interface{}) {
	r.Warning(reason, fmt.Sprintf(messageFmt, args...))
}

// backoffEventRecorder wraps a library-go events.Recorder.
//
// Keeps track of the rate of events during a "short" and "long" window
// of time. If the rate of events is excessive in either window, stop
// recording events for the backoff duration and keep in memory instead.
// Once the backoff duration has passed, record the in memory events
// in summary events grouped by event reason, and resume recording events
// normally.
type backoffEventRecorder struct {
	// the wrapped event recorder
	recorder events.Recorder

	// lock must be held to update any internal state
	lock sync.Mutex

	// events waiting to be recorded
	events map[string]map[string][]eventInfo

	// short excessive event window
	shortWindow         time.Duration
	shortWindowCountMax int

	// excessive event long window
	longWindow         time.Duration
	longWindowCountMax int

	// backoff delay when excessive events detected
	backoff time.Duration

	// keep count of events during windows
	shortWindowCount int
	longWindowCount  int

	// tickers
	shortWindowTicker *time.Ticker
	longWindowTicker  *time.Ticker

	// backoffTicker is nil if no backoff delay is in progress
	backoffTicker *time.Ticker
}

// eventInfo correlates an event message with the time when the event recording was requested.
type eventInfo struct {
	timestamp time.Time
	message   string
}

func (e eventInfo) String() string {
	return fmt.Sprintf("%s: %s", e.timestamp.Format(time.RFC3339), e.message)
}

// event records the event, buffers it in-memory, or emits it as part of a summary event.
func (r *backoffEventRecorder) event(eventType, reason, message string) {
	r.lock.Lock()
	defer r.lock.Unlock()

	// add event to buffer
	if r.events == nil {
		r.events = make(map[string]map[string][]eventInfo)
	}
	if _, ok := r.events[eventType]; !ok {
		r.events[eventType] = map[string][]eventInfo{}
	}
	r.events[eventType][reason] = append(r.events[eventType][reason], eventInfo{
		timestamp: time.Now(),
		message:   message,
	})

	// handle backoff period
	if r.backoffTicker != nil {
		select {
		case <-r.backoffTicker.C:
			// backoff period has passed
			klog.V(1).Info("Resuming connectivity event recording.")
			r.backoffTicker = nil
			r.longWindowCount = 0
			r.shortWindowCount = 0
		default:
			// still in backoff period
			return
		}
	}

	// update window event count
	select {
	case <-r.shortWindowTicker.C:
		r.shortWindowCount = 0
	default:
		r.shortWindowCount++
	}
	select {
	case <-r.longWindowTicker.C:
		r.longWindowCount = 0
	default:
		r.longWindowCount++
	}

	// if events are coming in too quickly, start backoff
	if r.shortWindowCount > r.shortWindowCountMax || r.longWindowCount > r.longWindowCountMax {
		if r.shortWindowCount > r.shortWindowCountMax {
			klog.V(1).Infof("More than %d events (%d) in the last %v.", r.shortWindowCountMax, r.shortWindowCount, r.shortWindow)
		} else {
			klog.V(1).Infof("More than %d events (%d) in the last %v.", r.longWindowCountMax, r.longWindowCount, r.longWindow)
		}
		klog.V(1).Infof("Backing off event recording for the next %v.", r.backoff)
		r.backoffTicker = time.NewTicker(r.backoff)
		return
	}

	// if we made it this far, record the buffered events
	for eventType, typeEvents := range r.events {
		for reason, reasonEvents := range typeEvents {
			sort.Slice(reasonEvents, func(i, j int) bool {
				return reasonEvents[i].timestamp.Before(reasonEvents[j].timestamp)
			})
			messages := joinEventMessages(reasonEvents)
			switch eventType {
			case corev1.EventTypeNormal:
				r.recorder.Event(reason, messages)
			case corev1.EventTypeWarning:
				r.recorder.Warning(reason, messages)
			}
		}
	}
	r.events = map[string]map[string][]eventInfo{}

}

func joinEventMessages(eventInfos []eventInfo) string {
	switch {
	case len(eventInfos) == 0:
		return ""
	case len(eventInfos) == 1:
		return eventInfos[0].message
	}
	var builder strings.Builder
	builder.WriteString(eventInfos[0].String())
	for _, eventInfo := range eventInfos[1:] {
		builder.WriteString("\n")
		builder.WriteString(eventInfo.String())
	}
	return builder.String()
}
