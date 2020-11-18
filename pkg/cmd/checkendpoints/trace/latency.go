package trace

import (
	"context"
	"net/http/httptrace"
	"time"

	"k8s.io/klog/v2"
)

type LatencyInfo struct {
	DNS          time.Duration
	Connect      time.Duration
	DNSStart     time.Time
	ConnectStart time.Time
}

func (r *LatencyInfo) dnsStart() {
	r.DNSStart = time.Now()
}

func (r *LatencyInfo) connectStart(addr string) {
	if r.ConnectStart.IsZero() {
		r.ConnectStart = time.Now()
	}
}

func (r *LatencyInfo) dnsDone() {
	r.DNS = time.Now().Sub(r.DNSStart)
}

func (r *LatencyInfo) connectDone(addr string) {
	r.Connect = time.Now().Sub(r.ConnectStart)
}

func WithLatencyInfoCapture(ctx context.Context) (context.Context, *LatencyInfo) {
	trace := &LatencyInfo{}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			trace.dnsStart()
			klog.V(5).Infof("DNSStart: %s\n", info.Host)
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			trace.dnsDone()
			klog.V(5).Infof("DNSDone: %v\n", info)
		},
		ConnectStart: func(network, addr string) {
			trace.connectStart(addr)
			klog.V(5).Infof("ConnectStart: %s %s\n", network, addr)
		},
		ConnectDone: func(network, addr string, err error) {
			trace.connectDone(addr)
			klog.V(5).Infof("ConnectDone: %s,%s,%v\n", network, addr, err)
		},
	}), trace
}
