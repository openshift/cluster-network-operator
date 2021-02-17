package controller

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/mergepatch"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/trace"
)

func TestManageStatusLogs(t *testing.T) {
	testOpErr := &net.OpError{Op: "connect", Net: "tcp", Err: errors.New("test error")}
	testDNSErr := &net.OpError{Op: "connect", Net: "tcp", Err: &net.DNSError{Err: "test error", Name: "host"}}

	testCases := []struct {
		name              string
		err               error
		trace             *trace.LatencyInfo
		initial           *v1alpha1.PodNetworkConnectivityCheckStatus
		expected          *v1alpha1.PodNetworkConnectivityCheckStatus
		expectedTimestamp time.Time
	}{
		{
			name: "TCPConnect",
			trace: &trace.LatencyInfo{
				ConnectStart: testTime(0),
				Connect:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(),
			expected: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(0)),
			),
			expectedTimestamp: testTime(0),
		},
		{
			name: "DNSResolve",
			trace: &trace.LatencyInfo{
				DNSStart:     testTime(0),
				DNS:          1 * time.Millisecond,
				ConnectStart: testTime(1),
				Connect:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(),
			expected: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(1)),
				withSuccessEntry(dnsResolveEntry(0)),
			),
			expectedTimestamp: testTime(0),
		},
		{
			name: "DNSError",
			err:  testDNSErr,
			trace: &trace.LatencyInfo{
				DNSStart: testTime(0),
				DNS:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(),
			expected: podNetworkConnectivityCheckStatus(
				withFailureEntry(dnsErrorEntry(0)),
			),
			expectedTimestamp: testTime(0),
		},
		{
			name: "TCPConnectError",
			err:  testOpErr,
			trace: &trace.LatencyInfo{
				ConnectStart: testTime(0),
				Connect:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(),
			expected: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(0)),
			),
			expectedTimestamp: testTime(0),
		},
		{
			name: "DNSResolveTCPConnectError",
			err:  testOpErr,
			trace: &trace.LatencyInfo{
				DNSStart:     testTime(0),
				DNS:          1 * time.Millisecond,
				ConnectStart: testTime(1),
				Connect:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(),
			expected: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(1)),
				withSuccessEntry(dnsResolveEntry(0)),
			),
			expectedTimestamp: testTime(0),
		},
		{
			name: "SuccessSort",
			trace: &trace.LatencyInfo{
				DNSStart:     testTime(3),
				DNS:          1 * time.Millisecond,
				ConnectStart: testTime(4),
				Connect:      1 * time.Millisecond,
			},
			initial: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(2)),
				withSuccessEntry(tcpConnectEntry(1)),
				withSuccessEntry(tcpConnectEntry(0)),
			),
			expected: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(4)),
				withSuccessEntry(dnsResolveEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withSuccessEntry(tcpConnectEntry(1)),
				withSuccessEntry(tcpConnectEntry(0)),
			),
			expectedTimestamp: testTime(3),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.initial
			updateStatusFuncs, timestamp := manageStatusLogs(&v1alpha1.PodNetworkConnectivityCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-to-target-endpoint",
				},
				Spec: v1alpha1.PodNetworkConnectivityCheckSpec{
					TargetEndpoint: "host:port",
				},
			}, tc.err, tc.trace)
			for _, updateStatusFunc := range updateStatusFuncs {
				updateStatusFunc(status)
			}
			assert.Equal(t, tc.expected, status)
			assert.Equal(t, tc.expectedTimestamp, timestamp)
		})
	}
}

func TestManageStatusOutage(t *testing.T) {
	//testOpErr := &net.OpError{Op: "connect", Net: "tcp", Err: errors.New("test error")}
	testCases := []struct {
		name     string
		err      error
		initial  *v1alpha1.PodNetworkConnectivityCheckStatus
		expected []v1alpha1.OutageEntry
	}{
		{
			name:    "NoLogs",
			initial: podNetworkConnectivityCheckStatus(),
		},
		{
			name: "FirstEntryIsSuccess",
			initial: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(1)),
			),
		},
		{
			name: "FirstEntryIsFailure",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(1)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(1, withOutageDetectedMessage(1),
					withStartLogEntry(tcpConnectErrorEntry(1)),
					withEndLogEntry(tcpConnectErrorEntry(1)),
				),
			},
		},
		{
			name: "NoLogsStartedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withOutageEntry(0),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(0),
			},
		},
		{
			name: "NoLogsEndedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withOutageEntry(0, withEnd(1)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(0, withEnd(1)),
			},
		},
		{
			name: "FailureLogNoOutage",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withSuccessEntry(tcpConnectEntry(1)),
				withSuccessEntry(tcpConnectEntry(0)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(3, withOutageDetectedMessage(3),
					withStartLogEntry(tcpConnectErrorEntry(3)),
					withEndLogEntry(tcpConnectErrorEntry(3)),
				),
			},
		},
		{
			name: "SuccessLogStartedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(4)),
				withFailureEntry(tcpConnectErrorEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withSuccessEntry(tcpConnectEntry(1)),
				withSuccessEntry(tcpConnectEntry(0)),
				withOutageEntry(3, withStartLogEntry(tcpConnectErrorEntry(3))),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(3, withEnd(4), withConnectivityRestoredMessage(3, 4),
					withStartLogEntry(tcpConnectErrorEntry(3)),
					withEndLogEntry(tcpConnectEntry(4)),
				),
			},
		},
		{
			name: "ErrorLogEndedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(5)),
				withSuccessEntry(tcpConnectEntry(4)),
				withSuccessEntry(tcpConnectEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withOutageEntry(0, withEnd(1)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(5, withOutageDetectedMessage(5),
					withStartLogEntry(tcpConnectErrorEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(5)),
				),
				*outageEntry(0, withEnd(1)),
			},
		},
		{
			name: "ErrorLogDuplicateStartedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(6)),
				withFailureEntry(tcpConnectErrorEntry(5)),
				withSuccessEntry(tcpConnectEntry(4)),
				withSuccessEntry(tcpConnectEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withOutageEntry(5, withOutageDetectedMessage(5),
					withStartLogEntry(tcpConnectErrorEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(5)),
				),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(5, withOutageDetectedMessage(5),
					withStartLogEntry(tcpConnectErrorEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(6)),
					withEndLogEntry(tcpConnectErrorEntry(5)),
				),
			},
		},
		{
			name: "ErrorLogChangedStartedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(6, withLogMessage("no route to host"))),
				withFailureEntry(tcpConnectErrorEntry(5)),
				withSuccessEntry(tcpConnectEntry(4)),
				withSuccessEntry(tcpConnectEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withOutageEntry(5, withOutageDetectedMessage(5),
					withStartLogEntry(tcpConnectErrorEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(5)),
				),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(5, withOutageDetectedMessage(5),
					withStartLogEntry(tcpConnectErrorEntry(6, withLogMessage("no route to host"))),
					withStartLogEntry(tcpConnectErrorEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(6, withLogMessage("no route to host"))),
					withEndLogEntry(tcpConnectErrorEntry(5)),
				),
			},
		},
		{
			name: "SuccessLogEndedOutageStartedOutage",
			initial: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(6)),
				withFailureEntry(tcpConnectErrorEntry(5)),
				withSuccessEntry(tcpConnectEntry(4)),
				withSuccessEntry(tcpConnectEntry(3)),
				withSuccessEntry(tcpConnectEntry(2)),
				withOutageEntry(5),
				withOutageEntry(0, withEnd(1)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(5, withEnd(6), withConnectivityRestoredMessage(5, 6),
					withEndLogEntry(tcpConnectEntry(6)),
				),
				*outageEntry(0, withEnd(1)),
			},
		},
		{
			name: "ErrorLogOngoingOutageMaxLogs",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(5, withLogMessage("five"))),
				withOutageEntry(0,
					withStartLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withStartLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withStartLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withStartLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withStartLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
					withEndLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withEndLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withEndLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withEndLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withEndLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
				),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(0,
					withStartLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withStartLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withStartLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withStartLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withStartLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
					withEndLogEntry(tcpConnectErrorEntry(5, withLogMessage("five"))),
					withEndLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withEndLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withEndLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withEndLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
				),
			},
		},
		{
			name: "SuccessLogOngoingOutageMaxLogs",
			initial: podNetworkConnectivityCheckStatus(
				withSuccessEntry(tcpConnectEntry(5)),
				withOutageEntry(0,
					withStartLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withStartLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withStartLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withStartLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withStartLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
					withEndLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withEndLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withEndLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withEndLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withEndLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
				),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(0, withEnd(5), withConnectivityRestoredMessage(0, 5),
					withStartLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withStartLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withStartLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withStartLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
					withStartLogEntry(tcpConnectErrorEntry(0, withLogMessage("zero"))),
					withEndLogEntry(tcpConnectEntry(5)),
					withEndLogEntry(tcpConnectErrorEntry(4, withLogMessage("four"))),
					withEndLogEntry(tcpConnectErrorEntry(3, withLogMessage("three"))),
					withEndLogEntry(tcpConnectErrorEntry(2, withLogMessage("two"))),
					withEndLogEntry(tcpConnectErrorEntry(1, withLogMessage("one"))),
				),
			},
		},
		{
			name: "MaxOutageEntries",
			initial: podNetworkConnectivityCheckStatus(
				withFailureEntry(tcpConnectErrorEntry(50)),
				withOutageEntry(48, withEnd(49)),
				withOutageEntry(46, withEnd(47)),
				withOutageEntry(44, withEnd(45)),
				withOutageEntry(42, withEnd(43)),
				withOutageEntry(40, withEnd(41)),
				withOutageEntry(38, withEnd(39)),
				withOutageEntry(36, withEnd(37)),
				withOutageEntry(34, withEnd(35)),
				withOutageEntry(32, withEnd(33)),
				withOutageEntry(30, withEnd(31)),
				withOutageEntry(28, withEnd(29)),
				withOutageEntry(26, withEnd(27)),
				withOutageEntry(24, withEnd(25)),
				withOutageEntry(22, withEnd(23)),
				withOutageEntry(20, withEnd(21)),
				withOutageEntry(18, withEnd(19)),
				withOutageEntry(16, withEnd(17)),
				withOutageEntry(14, withEnd(15)),
				withOutageEntry(12, withEnd(13)),
				withOutageEntry(10, withEnd(11)),
			),
			expected: []v1alpha1.OutageEntry{
				*outageEntry(50,
					withOutageDetectedMessage(50),
					withStartLogEntry(tcpConnectErrorEntry(50)),
					withEndLogEntry(tcpConnectErrorEntry(50)),
				),
				*outageEntry(48, withEnd(49)),
				*outageEntry(46, withEnd(47)),
				*outageEntry(44, withEnd(45)),
				*outageEntry(42, withEnd(43)),
				*outageEntry(40, withEnd(41)),
				*outageEntry(38, withEnd(39)),
				*outageEntry(36, withEnd(37)),
				*outageEntry(34, withEnd(35)),
				*outageEntry(32, withEnd(33)),
				*outageEntry(30, withEnd(31)),
				*outageEntry(28, withEnd(29)),
				*outageEntry(26, withEnd(27)),
				*outageEntry(24, withEnd(25)),
				*outageEntry(22, withEnd(23)),
				*outageEntry(20, withEnd(21)),
				*outageEntry(18, withEnd(19)),
				*outageEntry(16, withEnd(17)),
				*outageEntry(14, withEnd(15)),
				*outageEntry(12, withEnd(13)),
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.initial
			manageStatusOutage(events.NewInMemoryRecorder(t.Name()))(status)
			assert.Equal(t, tc.expected, status.Outages)
			if t.Failed() {
				t.Log("\n", mergepatch.ToYAMLOrError(tc.expected))
				t.Log("\n", mergepatch.ToYAMLOrError(status))
			}
		})
	}

}

func testTime(sec int) time.Time {
	return time.Date(2000, 1, 1, 0, 0, sec, 0, time.UTC)
}

func podNetworkConnectivityCheckStatus(options ...func(status *v1alpha1.PodNetworkConnectivityCheckStatus)) *v1alpha1.PodNetworkConnectivityCheckStatus {
	result := &v1alpha1.PodNetworkConnectivityCheckStatus{}
	for _, f := range options {
		f(result)
	}
	return result
}

func withSuccessEntry(entry v1alpha1.LogEntry) func(*v1alpha1.PodNetworkConnectivityCheckStatus) {
	return func(status *v1alpha1.PodNetworkConnectivityCheckStatus) {
		status.Successes = append(status.Successes, entry)
	}
}

func withFailureEntry(entry v1alpha1.LogEntry) func(*v1alpha1.PodNetworkConnectivityCheckStatus) {
	return func(status *v1alpha1.PodNetworkConnectivityCheckStatus) {
		status.Failures = append(status.Failures, entry)
	}
}

func tcpConnectErrorEntry(start int, options ...func(entry *v1alpha1.LogEntry)) v1alpha1.LogEntry {
	return logEntry(false, start, v1alpha1.LogEntryReasonTCPConnectError, "target-endpoint: failed to establish a TCP connection to host:port: connect tcp: test error", options...)
}

func dnsErrorEntry(start int, options ...func(entry *v1alpha1.LogEntry)) v1alpha1.LogEntry {
	return logEntry(false, start, v1alpha1.LogEntryReasonDNSError, "target-endpoint: failure looking up host host: connect tcp: lookup host: test error", options...)
}

func dnsResolveEntry(start int, options ...func(entry *v1alpha1.LogEntry)) v1alpha1.LogEntry {
	return logEntry(true, start, v1alpha1.LogEntryReasonDNSResolve, "target-endpoint: resolved host name host successfully", options...)
}

func tcpConnectEntry(start int, options ...func(entry *v1alpha1.LogEntry)) v1alpha1.LogEntry {
	return logEntry(true, start, v1alpha1.LogEntryReasonTCPConnect, "target-endpoint: tcp connection to host:port succeeded", options...)
}

func withLogMessage(message string) func(*v1alpha1.LogEntry) {
	return func(entry *v1alpha1.LogEntry) {
		entry.Message = message
	}
}

func outageEntry(start int, options ...func(entry *v1alpha1.OutageEntry)) *v1alpha1.OutageEntry {
	result := &v1alpha1.OutageEntry{Start: metav1.NewTime(testTime(start))}
	for _, f := range options {
		f(result)
	}
	return result
}

func withStartLogEntry(entry v1alpha1.LogEntry) func(*v1alpha1.OutageEntry) {
	return func(status *v1alpha1.OutageEntry) {
		status.StartLogs = append(status.StartLogs, entry)
	}
}

func withEndLogEntry(entry v1alpha1.LogEntry) func(*v1alpha1.OutageEntry) {
	return func(status *v1alpha1.OutageEntry) {
		status.EndLogs = append(status.EndLogs, entry)
	}
}

func withEnd(end int) func(*v1alpha1.OutageEntry) {
	return func(entry *v1alpha1.OutageEntry) {
		entry.End = metav1.NewTime(testTime(end))
	}
}

func withOutageDetectedMessage(start int) func(*v1alpha1.OutageEntry) {
	return withOutageMessage("Connectivity outage detected at %v", testTime(start).Format(time.RFC3339Nano))
}

func withConnectivityRestoredMessage(start, end int) func(*v1alpha1.OutageEntry) {
	return withOutageMessage("Connectivity restored after %v", testTime(end).Sub(testTime(start)))
}

func withOutageMessage(msg string, args ...interface{}) func(*v1alpha1.OutageEntry) {
	return func(entry *v1alpha1.OutageEntry) {
		entry.Message = fmt.Sprintf(msg, args...)
	}
}

func withOutageEntry(start int, options ...func(entry *v1alpha1.OutageEntry)) func(status *v1alpha1.PodNetworkConnectivityCheckStatus) {
	return func(status *v1alpha1.PodNetworkConnectivityCheckStatus) {
		status.Outages = append(status.Outages, *outageEntry(start, options...))
	}
}

func logEntry(success bool, start int, reason, message string, options ...func(entry *v1alpha1.LogEntry)) v1alpha1.LogEntry {
	entry := v1alpha1.LogEntry{
		Start:   metav1.NewTime(testTime(start)),
		Success: success,
		Reason:  reason,
		Message: message,
		Latency: metav1.Duration{Duration: 1 * time.Millisecond},
	}
	for _, f := range options {
		f(&entry)
	}
	return entry
}
