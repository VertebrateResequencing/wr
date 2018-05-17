// Copyright © 2016-2018 Genome Research Limited
// Author: Theo Barber-Bany <tb15@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package scheduler

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"k8s.io/client-go/util/workqueue"
)

type Controller struct {
	libclient     *client.Kubernetesp
	kubeclientset kubernetes.Interface
	restconfig    *rest.Config
	podLister     corelisters.PodLister
	podSynced     cache.InformerSynced
	nodeLister    corelisters.NodeLister
	nodeSynced    cache.InformerSynced
	workqueue     workqueue.RateLimitingInterface
}

// NewController returns a new scheduler controller
func NewController(
	kubeclientset kubernetes.Interface,
	restconfig *rest.Config,
	libclient *client.Kubernetesp,
	kubeInformerFactory kubeinformers.SharedInformerFactory,

) *Controller {
	// obtain references to shared index informers for the pod and node
	// types.
	podInformer := kubeInformerFactory.Core().V1().Pods()
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()

	controller := &Controller{
		libclient:     libclient,
		kubeclientset: kubeclientset,
		restconfig:    restconfig,
		podLister:     podInformer.Lister(),
		podSynced:     podInformer.Informer().HasSynced,
		nodeLister:    nodeInformer.Lister(),
		nodeSynced:    nodeInformer.Informer().HasSynced,
		workqueue:     workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}

	// Set up event handlers
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newPod := new.(*corev1.Pod)
			oldPod := old.(*corev1.Pod)
			if newPod.ResourceVersion == oldPod.ResourceVersion {
				// Periodic resync will send update events for all known pods
				// if they're different they will have different RVs
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newNode := new.(*corev1.Node)
			oldNode := old.(*corev1.Node)
			if newNode.ResourceVersion == oldNode.ResourceVersion {
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	return controller
}

// Run sets up event handlers, synces informer caches and starts workers
// blocks until stopCh is closed, at which point it'll shut down workqueue
// and wait for workers to finish processing.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start informer factories, begin populating informer caches.

	// Wait for caches to sync before starting workers
	if ok := cache.WaitForCacheSync(stopCh, c.podSynced, c.nodeSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	// start workers
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh

	return nil
}

// continually call processNextWorkItem to
// read and process th enext message on the workqueue
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem reads a single work item off the workqueue and
// attempts to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// Wrap this block in func so can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// Call done so workqueue knows we have
		// finished processing this item
		// Must also call forget if we don't want the
		// item re-queued.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// strings come off workqueue: namespace/name.
		// delayed nature of wq means items in informer cache
		// may be more up to date than the item initially
		// put in the wq.
		if key, ok = obj.(string); !ok {
			// As item in workqueue is invalid, call forget
			// else would loop attempting to process an
			// invalid work item.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}

		// Run syncHandler, passing it the namespace/name key of the resource to be synced
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s' : %s", key, err.Error())
		}
		// Finally, if no error occurs forget the item so it's not queued again
		c.workqueue.Forget(obj)
		return nil

	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}
	return true
}

// syncHandler compares the actual state with the desired, and attempts
// to converge the two
func (c *Controller) syncHandler(key string) error {
	return nil
}

func (c *Controller) handleObject(obj interface{}) {}
