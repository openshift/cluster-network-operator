package controller

import (
	"testing"
	"time"

	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/operatorcontrolplane/podnetworkconnectivitycheck/v1alpha1helpers"
	"github.com/stretchr/testify/assert"
)

func TestUpdatesManager_Add(t *testing.T) {

	testcases := []struct {
		name            string
		lastTimestamp   time.Time
		timestamps      []time.Time
		expectedUpdates []time.Time
	}{
		{
			name: "FirstUpdatesDelayed",
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(13),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
			},
			expectedUpdates: nil,
		},
		{
			name: "FirstUpdatesDelayedAndProcessed",
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(13),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
				testTime(23),
			},
			expectedUpdates: []time.Time{
				testTime(11),
				testTime(12),
				testTime(13),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
				testTime(23),
			},
		},
		{
			name:          "MissingUpdateUpdatesDelayed",
			lastTimestamp: testTime(10),
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
				testTime(23),
			},
			expectedUpdates: []time.Time{
				testTime(11),
				testTime(12),
			},
		},
		{
			name:          "MissingUpdateUpdatesDelayedAndProcessed",
			lastTimestamp: testTime(10),
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
				testTime(23),
				testTime(24),
				testTime(25),
				testTime(26),
			},
			expectedUpdates: []time.Time{
				testTime(11),
				testTime(12),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
				testTime(19),
				testTime(20),
				testTime(21),
				testTime(22),
				testTime(23),
				testTime(24),
				testTime(25),
				testTime(26),
			},
		},
		{
			name:          "OutOfOrder",
			lastTimestamp: testTime(10),
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(14),
				testTime(15),
				testTime(13),
			},
			expectedUpdates: []time.Time{
				testTime(11),
				testTime(12),
				testTime(13),
				testTime(14),
				testTime(15),
			},
		},
		{
			name:          "OutOfOrderOutOfOrder",
			lastTimestamp: testTime(10),
			timestamps: []time.Time{
				testTime(11),
				testTime(12),
				testTime(14),
				testTime(15),
				testTime(13),
				testTime(16),
				testTime(18),
				testTime(17),
			},
			expectedUpdates: []time.Time{
				testTime(11),
				testTime(12),
				testTime(13),
				testTime(14),
				testTime(15),
				testTime(16),
				testTime(17),
				testTime(18),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {

			updatesManager := updatesManager{
				checkPeriod:     1 * time.Second,
				checkTimeout:    10 * time.Second,
				sortingQueue:    map[time.Time][]v1alpha1helpers.UpdateStatusFunc{},
				lastTimestamp:   tc.lastTimestamp,
				processingQueue: nil,
			}

			var updateResults []time.Time

			for _, timestamp := range tc.timestamps {

				updatesManager.Add(
					timestamp, func(ts time.Time) v1alpha1helpers.UpdateStatusFunc {
						return func(_ *v1alpha1.PodNetworkConnectivityCheckStatus) {
							updateResults = append(updateResults, ts)
						}
					}(timestamp))
			}

			for _, update := range updatesManager.processingQueue {
				update(nil)
			}

			t.Log(updatesManager.processingQueue)
			t.Log(updateResults)

			assert.EqualValues(t, tc.expectedUpdates, updateResults)

		})
	}

}
