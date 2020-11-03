// Copyright 2018 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package leader

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// maxBackoffInterval defines the maximum amount of time to wait between
// attempts to become the leader.
const maxBackoffInterval = time.Second * 16

// BecomeLeader ensures that the current pod is the leader within its namespace. If
// run outside a cluster, it will skip leader election and return nil. It
// continuously tries to create a ConfigMap with the provided name and the
// current pod set as the owner reference. Only one can exist at a time with
// the same name, so the pod that successfully creates the ConfigMap is the
// leader. Upon termination of that pod, the garbage collector will delete the
// ConfigMap, enabling a different pod to become the leader.
func BecomeLeader(ctx context.Context, client crclient.Client, lockName string) error {
	klog.Info("Trying to become the leader.")

	ns, err := GetOperatorNamespace()
	if err != nil {
		if err == ErrNoNamespace || err == ErrRunLocal {
			klog.Info("Skipping leader election; not running in a cluster.")
			return nil
		}
		return err
	}

	owner, err := myOwnerRef(ctx, client, ns)
	if err != nil {
		return err
	}

	// check for existing lock from this pod, in case we got restarted
	existing := &corev1.ConfigMap{}
	key := crclient.ObjectKey{Namespace: ns, Name: lockName}
	err = client.Get(ctx, key, existing)

	switch {
	case err == nil:
		for _, existingOwner := range existing.GetOwnerReferences() {
			if existingOwner.Name == owner.Name {
				klog.Info("Found existing lock with my name. I was likely restarted.")
				klog.Info("Continuing as the leader.")
				return nil
			}
			klog.Infof("Found existing lock. LockOwner: %v", existingOwner.Name)
		}
	case apierrors.IsNotFound(err):
		klog.Info("No pre-existing lock was found.")
	default:
		klog.Infof("Unknown error trying to get ConfigMap: %v", err)
		return err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lockName,
			Namespace:       ns,
			OwnerReferences: []metav1.OwnerReference{*owner},
		},
	}

	// try to create a lock
	backoff := time.Second
	for {
		klog.V(2).Infof("Leader election: trying to create configmap %s/%s", cm.Namespace, cm.Name)
		err := client.Create(ctx, cm)
		switch {
		case err == nil:
			klog.V(1).Info("Became the leader.")
			return nil
		case apierrors.IsAlreadyExists(err):
			existingOwners := existing.GetOwnerReferences()
			switch {
			case len(existingOwners) != 1:
				klog.V(1).Infof("Leader lock configmap must have exactly one owner reference. ConfigMap: %v", existing)
			case existingOwners[0].Kind != "Pod":
				klog.V(1).Infof("Leader lock configmap owner reference must be a pod. OwnerReference: %v", existingOwners[0])
			default:
				leaderPod := &corev1.Pod{}
				key = crclient.ObjectKey{Namespace: ns, Name: existingOwners[0].Name}
				err = client.Get(ctx, key, leaderPod)
				switch {
				case apierrors.IsNotFound(err):
					klog.V(2).Info("Leader pod has been deleted, waiting for garbage collection to remove the lock.")
				case err != nil:
					return err
				case isPodEvicted(*leaderPod) && leaderPod.GetDeletionTimestamp() == nil:
					klog.Infof("Operator pod with leader lock has been evicted. leader: %v", leaderPod.Name)
					klog.Info("Deleting evicted leader.")
					// Pod may not delete immediately, continue with backoff
					err := client.Delete(ctx, leaderPod)
					if err != nil {
						klog.Infof("Leader pod could not be deleted: %v", err)
					}

				default:
					klog.Info("Not the leader. Waiting.")
				}
			}

			select {
			case <-time.After(wait.Jitter(backoff, .2)):
				if backoff < maxBackoffInterval {
					backoff *= 2
				}
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			klog.Infof("Unknown error creating ConfigMap: %v", err)
			return err
		}
	}
}

// myOwnerRef returns an OwnerReference that corresponds to the pod in which
// this code is currently running.
// It expects the environment variable POD_NAME to be set by the downwards API
func myOwnerRef(ctx context.Context, client crclient.Client, ns string) (*metav1.OwnerReference, error) {
	myPod, err := GetPod(ctx, client, ns)
	if err != nil {
		return nil, err
	}

	owner := &metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       myPod.ObjectMeta.Name,
		UID:        myPod.ObjectMeta.UID,
	}
	return owner, nil
}

func isPodEvicted(pod corev1.Pod) bool {
	podFailed := pod.Status.Phase == corev1.PodFailed
	podEvicted := pod.Status.Reason == "Evicted"
	return podFailed && podEvicted
}
