package controller

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/operatorcontrolplane/podnetworkconnectivitycheck/v1alpha1helpers"
)

// NewUpdatesManager returns a queue sorting UpdateManager.
func NewUpdatesManager(checkPeriod, checkTimeout time.Duration, processor UpdatesProcessor) UpdatesManager {
	return &updatesManager{
		checkPeriod:  checkPeriod,
		checkTimeout: checkTimeout,
		sortingQueue: map[time.Time][]v1alpha1helpers.UpdateStatusFunc{},
		processor:    processor,
	}
}

// UpdatesManager manages a queue of updates.
type UpdatesManager interface {
	// Add an update to the queue.
	Add(timestamp time.Time, updates ...v1alpha1helpers.UpdateStatusFunc)
	// Process the updates ready to be processed.
	Process(ctx context.Context, flush bool) error
}

// updateManage implements an UpdateManager that sorts the incoming updates, delaying updates
// that appear to have been received out of order.
type updatesManager struct {
	// lock for queues
	lock sync.Mutex
	// how often updates are expected to be added
	checkPeriod time.Duration
	// max amount of time a check should take to be added
	checkTimeout time.Duration
	// sortingQueue holds for sorting during the sorting checkTimeout.
	sortingQueue map[time.Time][]v1alpha1helpers.UpdateStatusFunc
	// order of updates in the sortingQueue
	timestamps []time.Time
	// timestamp of last set of updates sent to the processing queue
	lastTimestamp time.Time
	// updates ready to be processed
	processingQueue []v1alpha1helpers.UpdateStatusFunc
	// processor of updates
	processor UpdatesProcessor
}

type UpdatesProcessor func(context.Context, ...v1alpha1helpers.UpdateStatusFunc) error

// Add an update to the queue. There is a delay equal to the size of the sorting checkTimeout before
// updates are made available on the queue to allow for updates submitted out of order within
// the sorting checkTimeout to be sorted by timestamp.
func (u *updatesManager) Add(timestamp time.Time, updates ...v1alpha1helpers.UpdateStatusFunc) {
	u.lock.Lock()
	defer u.lock.Unlock()

	u.sortingQueue[timestamp] = updates

	u.timestamps = append(u.timestamps, timestamp)
	sort.Slice(u.timestamps, func(i, j int) bool {
		return u.timestamps[i].Before(u.timestamps[j])
	})

	latestTimestamp := u.timestamps[len(u.timestamps)-1]
	var requeue []time.Time
	for _, timestamp := range u.timestamps {
		switch {

		// updates came in quickly after last set of updates
		case timestamp.Sub(u.lastTimestamp) < u.checkPeriod*2:
			fallthrough

		// we seem to be missing updates, but we're not going to wait any longer
		case latestTimestamp.Sub(timestamp) > u.checkTimeout+u.checkPeriod:
			u.processingQueue = append(u.processingQueue, u.sortingQueue[timestamp]...)
			delete(u.sortingQueue, timestamp)
			u.lastTimestamp = timestamp

		// we seem to be missing a set of updates, requeue the current set of updates
		default:
			requeue = append(requeue, timestamp)
		}
	}
	u.timestamps = requeue
}

// Process updates and remove from the processing queue. If flush is true, updates not
// ready to be processed are also processed.
func (u *updatesManager) Process(ctx context.Context, flush bool) error {
	u.lock.Lock()
	defer u.lock.Unlock()
	if flush || len(u.processingQueue) > 20 {
		if err := u.processor(ctx, u.processingQueue...); err != nil {
			return err
		}
		u.processingQueue = nil
	}
	if flush {
		var updates []v1alpha1helpers.UpdateStatusFunc
		for _, timestamp := range u.timestamps {
			updates = append(updates, u.sortingQueue[timestamp]...)
		}
		if err := u.processor(ctx, updates...); err != nil {
			return err
		}
		u.sortingQueue = map[time.Time][]v1alpha1helpers.UpdateStatusFunc{}
		u.timestamps = nil
	}
	return nil
}
