package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/stretchr/testify/assert"
)

func TestWithShortWindow(t *testing.T) {

	shortDuration := 20 * time.Millisecond
	shortCountMax := 3
	longDuration := 2 * shortDuration
	longCountMax := 60
	backoffDuration := longDuration
	excessiveEventCount := 10

	inMemoryRecorder := events.NewInMemoryRecorder(t.Name())
	r := NewBackoffEventRecorder(inMemoryRecorder,
		WithShortWindow(shortDuration, shortCountMax),
		WithLongWindow(longDuration, longCountMax),
		WithBackoff(backoffDuration),
	)

	// first event
	r.Eventf(t.Name(), "TEST")
	// wait short window
	<-time.After(shortDuration)
	// excessive events for short window
	for i := 0; i < shortCountMax+excessiveEventCount; i++ {
		r.Eventf(t.Name(), "TEST")
	}
	// wait for backoff period to end
	<-time.After(backoffDuration)
	// some more events
	for i := 0; i < shortCountMax-1; i++ {
		r.Eventf(t.Name(), "TEST")
	}
	assert.Len(t, inMemoryRecorder.Events(), 1+shortCountMax+1+shortCountMax-1)
	assert.Len(t, strings.Split(inMemoryRecorder.Events()[1+shortCountMax+1].Message, "\n"), excessiveEventCount, "Summary event expected with 10 messages.")
	if t.Failed() {
		t.Logf("%2d | %15v %q", 0, time.Duration(0), inMemoryRecorder.Events()[0].Message)
		prev := inMemoryRecorder.Events()[0].LastTimestamp
		for i, event := range inMemoryRecorder.Events()[1:] {
			t.Logf("%2d | %15v %q", i+1, event.LastTimestamp.Sub(prev.Time), event.Message)
			prev = event.LastTimestamp
		}
	}

}

func TestWithLongWindow(t *testing.T) {

	shortDuration := 20 * time.Millisecond
	shortCountMax := 3
	longDuration := 5 * shortDuration
	longCountMax := (shortCountMax - 1) * 3
	backoffDuration := longDuration
	excessiveEventCount := 10

	inMemoryRecorder := events.NewInMemoryRecorder(t.Name())
	r := NewBackoffEventRecorder(inMemoryRecorder,
		WithShortWindow(shortDuration, shortCountMax),
		WithLongWindow(longDuration, longCountMax),
		WithBackoff(backoffDuration),
	)

	// fire enough events to trigger long window, slowly enough to no trigger short window
	for i := 0; i < longCountMax; {
		// start timer for short window
		afterShortDuration := time.After(shortDuration)
		// fire some events, not enough to trigger short window
		for i := 0; i < shortCountMax-1; i++ {
			r.Eventf(t.Name(), "TEST")
		}
		// wait short window
		<-afterShortDuration
		i += shortCountMax - 1
	}

	// excessive events for long window
	for i := 0; i < excessiveEventCount; i++ {
		r.Eventf(t.Name(), "TEST")
	}

	// wait for backoff period to end
	<-time.After(backoffDuration)

	// some more events
	for i := 0; i < 2; i++ {
		r.Eventf(t.Name(), "TEST")
	}

	assert.Len(t, inMemoryRecorder.Events(), longCountMax+1+1)
	assert.Len(t, strings.Split(inMemoryRecorder.Events()[longCountMax].Message, "\n"), excessiveEventCount+1, "Summary event expected with %d messages.", excessiveEventCount+1)
	if t.Failed() {
		t.Logf("%2d | %15v %q", 0, time.Duration(0), inMemoryRecorder.Events()[0].Message)
		prev := inMemoryRecorder.Events()[0].LastTimestamp
		for i, event := range inMemoryRecorder.Events()[1:] {
			t.Logf("%2d | %15v %q", i+1, event.LastTimestamp.Sub(prev.Time), event.Message)
			prev = event.LastTimestamp
		}
	}

}
