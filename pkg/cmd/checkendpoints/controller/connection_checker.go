package controller

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"time"

	operatorcontrolplanev1alpha1 "github.com/openshift/api/operatorcontrolplane/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/operatorcontrolplane/podnetworkconnectivitycheck/v1alpha1helpers"
	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/trace"
)

const (
	checkPeriod  = 1 * time.Minute
	checkTimeout = 10 * time.Second
)

// ConnectionChecker checks a single connection and updates status when appropriate
type ConnectionChecker interface {
	Run(ctx context.Context)
	Stop(ctx context.Context)
}

type GetCheckFunc func() *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck

// NewConnectionChecker returns a ConnectionChecker.
func NewConnectionChecker(name, podName, podNamespace string, getCheck GetCheckFunc, client v1alpha1helpers.PodNetworkConnectivityCheckClient, clientCertGetter CertificatesGetter, recorder Recorder) ConnectionChecker {
	return &connectionChecker{
		name:             name,
		podName:          podName,
		getCheck:         getCheck,
		client:           client,
		clientCertGetter: clientCertGetter,
		recorder:         recorder,
		updates:          NewUpdatesManager(checkPeriod, checkTimeout, newUpdatesProcessor(client, name)),
		stop:             make(chan interface{}),
		metrics:          NewMetricsContext(podNamespace, name),
	}
}

func newUpdatesProcessor(client v1alpha1helpers.PodNetworkConnectivityCheckClient, name string) UpdatesProcessor {
	return func(ctx context.Context, updates ...v1alpha1helpers.UpdateStatusFunc) error {
		_, _, err := v1alpha1helpers.UpdateStatus(ctx, client, name, updates...)
		return err
	}
}

type CertificatesGetter func() []tls.Certificate

type connectionChecker struct {
	name     string
	podName  string
	getCheck GetCheckFunc

	client           v1alpha1helpers.PodNetworkConnectivityCheckClient
	clientCertGetter CertificatesGetter
	recorder         Recorder
	updates          UpdatesManager
	stop             chan interface{}
	metrics          MetricsContext
}

// checkConnection checks the connection periodically, updating status as needed
func (c *connectionChecker) checkConnection(ctx context.Context) {
	ticker := time.NewTicker(checkPeriod)
	defer ticker.Stop()
	defer klog.V(1).Infof("Stopped connectivity check %s.", c.name)
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return

		case <-ticker.C:
			go func() {
				currCheck := c.getCheck()
				// if we have no check or the check isn't for us or the check has no target, report status if needed, but nothing else
				if currCheck == nil || currCheck.Spec.SourcePod != c.podName || len(currCheck.Spec.TargetEndpoint) == 0 {
					c.updateStatus(ctx, false)
					return
				}
				c.checkEndpoint(ctx, currCheck)
				c.updateStatus(ctx, false)
			}()
		}
	}
}

// Run starts the connection checker.
func (c *connectionChecker) Run(ctx context.Context) {
	ctx2, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx2.Done():
		}
	}()
	go wait.UntilWithContext(ctx2, func(ctx context.Context) {
		c.checkConnection(ctx2)
	}, checkPeriod)
	klog.V(1).Infof("Started connectivity check %s.", c.name)
	<-ctx2.Done()
}

// Stop
func (c *connectionChecker) Stop(ctx context.Context) {
	c.updateStatus(ctx, true)
	close(c.stop)
}

// updateStatus applies updates. If an error occurs applying an update,
// it remain on the queue and retried on the next call to updateStatus.
func (c *connectionChecker) updateStatus(ctx context.Context, flush bool) {
	if err := c.updates.Process(ctx, false); err != nil {
		klog.Warningf("Unable to update status of %s: %v", c.name, err)
	}
}

// checkEndpoint performs the check and manages the PodNetworkConnectivityCheck.Status changes that result.
func (c *connectionChecker) checkEndpoint(ctx context.Context, check *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck) {
	latencyInfo, err := c.getTCPConnectLatency(ctx, check.Spec.TargetEndpoint)
	statusUpdates, timestamp := manageStatusLogs(check, err, latencyInfo)
	if len(statusUpdates) > 0 {
		statusUpdates = append(statusUpdates, manageStatusOutage(c.recorder))
	}
	if len(statusUpdates) > 0 {
		statusUpdates = append(statusUpdates, manageStatusConditions)
	}
	c.updates.Add(timestamp, statusUpdates...)
}

// getTCPConnectLatency connects to a tcp endpoint and collects latency info
func (c *connectionChecker) getTCPConnectLatency(ctx context.Context, address string) (*trace.LatencyInfo, error) {
	klog.V(4).Infof("Check BEGIN: %v", address)
	defer klog.V(4).Infof("Check END  : %v", address)
	ctx, latencyInfo := trace.WithLatencyInfoCapture(ctx)

	// tcp connection
	dialer := &net.Dialer{
		Timeout: checkTimeout,
	}
	tcpConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		c.metrics.Update(address, latencyInfo, err)
		return latencyInfo, err
	}

	// perform tls handshake to avoid spamming the logs of tls endpoints
	host, _, _ := net.SplitHostPort(address)
	tlsConn := tls.Client(tcpConn, &tls.Config{Certificates: c.clientCertGetter(), ServerName: host, InsecureSkipVerify: true})
	if err = tlsConn.Handshake(); err != nil {
		// ignore any error. most likely non-tls connection, plus we're not really testing tls
		klog.V(4).Infof("%s: tls error ignored: %v", address, err)
		_ = tcpConn.Close()
		c.metrics.Update(address, latencyInfo, nil)
		return latencyInfo, nil
	}

	// gracefully close connection (ignore error)
	_ = tlsConn.Close()

	c.metrics.Update(address, latencyInfo, err)
	return latencyInfo, err
}

// isDNSError returns true if the cause of the net operation error is a DNS error
func isDNSError(err error) bool {
	if opErr, ok := err.(*net.OpError); ok {
		if _, ok := opErr.Err.(*net.DNSError); ok {
			return true
		}
	}
	return false
}

// manageStatusLogs returns status update functions that updates the PodNetworkConnectivityCheck.Status's
// Successes/Failures logs reflect the results of the check. The time that the check started is also returned.
func manageStatusLogs(check *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck, checkErr error, latency *trace.LatencyInfo) ([]v1alpha1helpers.UpdateStatusFunc, time.Time) {
	var statusUpdates []v1alpha1helpers.UpdateStatusFunc
	description := regexp.MustCompile(".*-to-").ReplaceAllString(check.Name, "")
	host, _, _ := net.SplitHostPort(check.Spec.TargetEndpoint)
	if isDNSError(checkErr) {
		klog.V(2).Infof("%7s | %-15s | %10s | Failure looking up host %s: %v", "Failure", "DNSError", latency.DNS, host, checkErr)
		return append(statusUpdates, v1alpha1helpers.AddFailureLogEntry(operatorcontrolplanev1alpha1.LogEntry{
			Start:   metav1.NewTime(latency.DNSStart),
			Success: false,
			Reason:  operatorcontrolplanev1alpha1.LogEntryReasonDNSError,
			Message: fmt.Sprintf("%s: failure looking up host %s: %v", description, host, checkErr),
			Latency: metav1.Duration{Duration: latency.DNS},
		})), latency.DNSStart
	}
	var overallStart time.Time
	if latency.DNS != 0 {
		klog.V(2).Infof("%7s | %-15s | %10s | Resolved host name %s successfully", "Success", "DNSResolve", latency.DNS, host)
		statusUpdates = append(statusUpdates, v1alpha1helpers.AddSuccessLogEntry(operatorcontrolplanev1alpha1.LogEntry{
			Start:   metav1.NewTime(latency.DNSStart),
			Success: true,
			Reason:  operatorcontrolplanev1alpha1.LogEntryReasonDNSResolve,
			Message: fmt.Sprintf("%s: resolved host name %s successfully", description, host),
			Latency: metav1.Duration{Duration: latency.DNS},
		}))
		overallStart = latency.DNSStart
	}
	if overallStart.IsZero() {
		overallStart = latency.ConnectStart
	}
	if checkErr != nil {
		klog.V(2).Infof("%7s | %-15s | %10s | Failed to establish a TCP connection to %s: %v", "Failure", "TCPConnectError", latency.Connect, check.Spec.TargetEndpoint, checkErr)
		return append(statusUpdates, v1alpha1helpers.AddFailureLogEntry(operatorcontrolplanev1alpha1.LogEntry{
			Start:   metav1.NewTime(latency.ConnectStart),
			Success: false,
			Reason:  operatorcontrolplanev1alpha1.LogEntryReasonTCPConnectError,
			Message: fmt.Sprintf("%s: failed to establish a TCP connection to %s: %v", description, check.Spec.TargetEndpoint, checkErr),
			Latency: metav1.Duration{Duration: latency.Connect},
		})), overallStart
	}
	klog.V(2).Infof("%7s | %-15s | %10s | TCP connection to %v succeeded", "Success", "TCPConnect", latency.Connect, check.Spec.TargetEndpoint)
	return append(statusUpdates, v1alpha1helpers.AddSuccessLogEntry(operatorcontrolplanev1alpha1.LogEntry{
		Start:   metav1.NewTime(latency.ConnectStart),
		Success: true,
		Reason:  operatorcontrolplanev1alpha1.LogEntryReasonTCPConnect,
		Message: fmt.Sprintf("%s: tcp connection to %s succeeded", description, check.Spec.TargetEndpoint),
		Latency: metav1.Duration{Duration: latency.Connect},
	})), overallStart
}

// manageStatusOutage returns a status update function that manages the
// PodNetworkConnectivityCheck.Status.Outage entries based on Successes/Failures log entries.
func manageStatusOutage(recorder Recorder) v1alpha1helpers.UpdateStatusFunc {
	return func(status *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus) {
		// This func is kept simple by assuming that only one log entry has been
		// added since the last time this method was invoked. See checkEndpoint func.
		var currentOutage *operatorcontrolplanev1alpha1.OutageEntry
		if len(status.Outages) > 0 && status.Outages[0].End.IsZero() {
			currentOutage = &status.Outages[0]
		}
		var latestFailure, latestSuccess operatorcontrolplanev1alpha1.LogEntry
		if len(status.Failures) > 0 {
			latestFailure = status.Failures[0]
		}
		if len(status.Successes) > 0 {
			latestSuccess = status.Successes[0]
		}
		switch {
		case currentOutage == nil && latestFailure.Start.After(latestSuccess.Start.Time):
			// outage started
			newOutage := operatorcontrolplanev1alpha1.OutageEntry{
				Start:     latestFailure.Start,
				StartLogs: []operatorcontrolplanev1alpha1.LogEntry{latestFailure},
				EndLogs:   []operatorcontrolplanev1alpha1.LogEntry{latestFailure},
				Message:   fmt.Sprintf("Connectivity outage detected at %v", latestFailure.Start.Format(time.RFC3339Nano)),
			}
			status.Outages = append([]operatorcontrolplanev1alpha1.OutageEntry{newOutage}, status.Outages...)
			recorder.Warningf("ConnectivityOutageDetected", "Connectivity outage detected: %s", latestFailure.Message)
		case currentOutage != nil && latestFailure.Start.After(latestSuccess.Start.Time):
			// outage ongoing, add failure to start and end logs
			switch {
			case len(currentOutage.StartLogs) == 0:
				// not expected since new outages should always have at least one start log entry.
				fallthrough
			case len(currentOutage.StartLogs) < 5 && currentOutage.StartLogs[0].Message != latestFailure.Message:
				// append (up to 5) failure log entry to start log if failure reason/message has changed
				currentOutage.StartLogs = append([]operatorcontrolplanev1alpha1.LogEntry{latestFailure}, currentOutage.StartLogs...)
			}
			// append failure log entry to end log
			currentOutage.EndLogs = append([]operatorcontrolplanev1alpha1.LogEntry{latestFailure}, currentOutage.EndLogs...)
			if len(currentOutage.EndLogs) > 5 {
				// limit end log to 5 latest entries
				currentOutage.EndLogs = currentOutage.EndLogs[:5]
			}
		case currentOutage != nil && latestSuccess.Start.After(latestFailure.Start.Time):
			// outage ended
			currentOutage.End = latestSuccess.Start
			outageDuration := currentOutage.End.Sub(currentOutage.Start.Time)
			currentOutage.EndLogs = append([]operatorcontrolplanev1alpha1.LogEntry{latestSuccess}, currentOutage.EndLogs...)
			if len(currentOutage.EndLogs) > 5 {
				currentOutage.EndLogs = currentOutage.EndLogs[:5]
			}
			currentOutage.Message = fmt.Sprintf("Connectivity restored after %v", outageDuration)
			recorder.Eventf("ConnectivityRestored", "Connectivity restored after %v: %s", outageDuration, latestSuccess.Message)
		default:
			// no outage in progress
		}
		if len(status.Outages) > 20 {
			status.Outages = status.Outages[:20]
		}
	}
}

// manageStatusConditions returns a status update function that set the appropriate conditions on the
// PodNetworkConnectivityCheck.
func manageStatusConditions(status *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus) {
	reachableCondition := operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckCondition{
		Type:   operatorcontrolplanev1alpha1.Reachable,
		Status: metav1.ConditionUnknown,
	}
	if len(status.Outages) == 0 || !status.Outages[0].End.IsZero() {
		var latestSuccessLogEntry operatorcontrolplanev1alpha1.LogEntry
		if len(status.Successes) > 0 {
			latestSuccessLogEntry = status.Successes[0]
		}
		reachableCondition.Status = metav1.ConditionTrue
		reachableCondition.Reason = "TCPConnectSuccess"
		reachableCondition.Message = latestSuccessLogEntry.Message
	} else {
		var latestFailureLogEntry operatorcontrolplanev1alpha1.LogEntry
		if len(status.Failures) > 0 {
			latestFailureLogEntry = status.Failures[0]
		}
		reachableCondition.Status = metav1.ConditionFalse
		reachableCondition.Reason = latestFailureLogEntry.Reason
		reachableCondition.Message = latestFailureLogEntry.Message
	}
	v1alpha1helpers.SetPodNetworkConnectivityCheckCondition(&status.Conditions, reachableCondition)
}
