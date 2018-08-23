// Copyright © 2018 Genome Research Limited
// Author: Theo Barber-Bany
// <tb15@sanger.ac.uk>.
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

/*
Package scheduler a kubernetes controller to oversee the scheduling of wr-runner
pods to a kubernetes cluster, It is an adapter that communicates wit the
scheduleri implementation in ../../jobqueue/scheduler/kubernetes.go, and
provides answers to questions that require up to date information about what is
going on inside a cluster.
*/

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/kubernetes/client"
	"github.com/inconshreveable/log15"
	"github.com/sb10/l15h"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"k8s.io/client-go/util/workqueue"
)

const (
	// maxRetries is the number of times a deployment will be retried before it
	// is dropped out of the queue. With the current rate-limiter in use
	// (5ms*2^(maxRetries-1)) the following numbers represent the times a work
	// item is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s,
	// 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	// kubeSchedulerControllerLog is the file name to save logs to
	kubeSchedulerControllerLog = "kubeSchedulerControllerLog"
)

// Controller stores everything the controller needs
type Controller struct {
	libclient         *client.Kubernetesp // Our client library
	kubeclientset     kubernetes.Interface
	restconfig        *rest.Config
	podLister         corelisters.PodLister
	podSynced         cache.InformerSynced
	nodeLister        corelisters.NodeLister
	nodeSynced        cache.InformerSynced
	workqueue         workqueue.RateLimitingInterface
	opts              ScheduleOpts // Options for the scheduler
	nodeResources     map[nodeName]corev1.ResourceList
	nodeResourcesLen  *int
	nodeResourceMutex *sync.RWMutex
	podAliveMap       *sync.Map
	log15.Logger
}

// ScheduleOpts stores options  for the scheduler
type ScheduleOpts struct {
	Files        []client.FilePair  // Files to copy to each spawned runner. Potentially listen on a channel later.
	CbChan       chan string        // Channel to send errors on
	BadCbChan    chan *cloud.Server // Send bad 'servers' back to wr.
	ReqChan      chan *Request      // Channel to send requests about resource availability to
	PodAliveChan chan *PodAlive     // Channel to send PodAlive requests
	ManagerDir   string             // Directory to store logs in
	Logger       log15.Logger
}

// Request contains relevant information for processing a request from
// reqCheck().
type Request struct {
	RAM    resource.Quantity
	Time   time.Duration
	Cores  resource.Quantity
	Disk   resource.Quantity
	Other  map[string]string
	CbChan chan Response
}

// Response contains relevant information for responding to a Request from
// reqCheck()
type Response struct {
	Error     error
	Ephemeral bool // indicate if ephemeral storage is enabled

}

// PodAlive contains a pod, and a chan error that is notified when the pod
// terminates.
type PodAlive struct {
	Pod     *corev1.Pod
	ErrChan chan error
	Done    bool
}

type nodeName string

// NewController returns a new scheduler controller
func NewController(
	kubeclientset kubernetes.Interface,
	restconfig *rest.Config,
	libclient *client.Kubernetesp,
	kubeInformerFactory kubeinformers.SharedInformerFactory,
	opts ScheduleOpts,

) *Controller {
	// obtain references to shared index informers for the pod and node types.
	podInformer := kubeInformerFactory.Core().V1().Pods()
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()

	controller := &Controller{
		libclient:         libclient,
		kubeclientset:     kubeclientset,
		restconfig:        restconfig,
		podLister:         podInformer.Lister(),
		podSynced:         podInformer.Informer().HasSynced,
		nodeLister:        nodeInformer.Lister(),
		nodeSynced:        nodeInformer.Informer().HasSynced,
		workqueue:         workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		opts:              opts,
		nodeResources:     make(map[nodeName]corev1.ResourceList),
		nodeResourceMutex: new(sync.RWMutex),
	}

	// Set up event handlers Only watch pods with the label 'app=wr-runner'
	podInformer.Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				pod := obj.(*corev1.Pod)
				return pod.ObjectMeta.Labels["wr"] == "runner"
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
					if err != nil {
						utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", obj, err))
						return
					}
					controller.workqueue.Add(key)
				},
				UpdateFunc: func(old, new interface{}) {
					newPod := new.(*corev1.Pod)
					oldPod := old.(*corev1.Pod)
					if newPod.ResourceVersion == oldPod.ResourceVersion {
						// Periodic resync will send update events for all known
						// pods if they're different they will have different
						// RVs
						return
					}
					key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(new)
					if err != nil {
						utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", new, err))
						return
					}
					controller.workqueue.Add(key)

				},
				DeleteFunc: func(obj interface{}) {
					pod, ok := obj.(*corev1.Pod)
					if !ok {
						utilruntime.HandleError(fmt.Errorf("Couldn't cast object to pod for object %#v", obj))
						return
					}
					controller.Debug("pod deleted", "pod", pod.ObjectMeta.Name)
				},
			},
		})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", obj, err))
				return
			}
			controller.workqueue.Add(key)
		},
		UpdateFunc: func(old, new interface{}) {
			newPod := new.(*corev1.Node)
			oldPod := old.(*corev1.Node)
			if newPod.ResourceVersion == oldPod.ResourceVersion {
				// Periodic resync will send update events for all known nodes
				// if they're different they will have different RVs
				return
			}
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(new)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", new, err))
				return
			}
			controller.workqueue.Add(key)

		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("Couldn't cast object to node for object %#v", obj))
				return
			}
			// Delete the node from the nodeResources map. Do it here, so it's
			// not an item added to the queue.
			delete(controller.nodeResources, nodeName(node.ObjectMeta.Name))
		},
	})

	// Set up the map[pod.ObjectMeta.UID] errChan,  a unique channel for
	// handling errors related to the passed pod. Uses the sync.Map as it fits
	// use case 1 exactly: "The Map type is optimized for two common use cases:
	// (1) when the entry for a given key is only ever written once but read
	// many times.."
	controller.podAliveMap = new(sync.Map)

	return controller
}

// Run sets up event handlers, synces informer caches and starts workers. It
// blocks until stopCh is closed, at which point it'll shut down workqueue and
// wait for workers to finish processing.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	c.Logger = c.opts.Logger.New("schedulerController", "kubernetes")
	kubeLogFile := filepath.Join(c.opts.ManagerDir, kubeSchedulerControllerLog)
	fh, err := log15.FileHandler(kubeLogFile, log15.LogfmtFormat())
	if err != nil {
		return fmt.Errorf("wr kubernetes scheduler could not log to %s: %s", kubeLogFile, err)
	}

	l15h.AddHandler(c.Logger, fh)
	c.Debug("In Run()")
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start informer factories, begin populating informer caches.

	// Wait for caches to sync before starting workers
	c.Debug("Waiting for caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.podSynced, c.nodeSynced); !ok {
		c.Crit("failed to wait for caches to sync")
		utilruntime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return fmt.Errorf("failed to wait for caches to sync")
	}
	c.Debug("Caches synced")

	c.Debug("In Run(), starting workers")
	// start workers
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runReqCheck, time.Second, stopCh)
		go wait.Until(c.runPodAlive, time.Second, stopCh)
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh

	return nil
}

// continually call processNextWorkItem to read and process the next message on
// the workqueue
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem reads a single work item off the workqueue and attempts
// to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	// Call done so workqueue knows we have finished processing this item
	// Must also call forget if we don't want the item re-queued.
	defer c.workqueue.Done(obj)
	var key string
	var ok bool
	// strings come off workqueue: namespace/name. delayed nature of wq
	// means items in informer cache may be more up to date than the item
	// initially put in the wq.
	if key, ok = obj.(string); !ok {
		// As item in workqueue is invalid, call forget else would loop
		// attempting to process an invalid work item.
		c.workqueue.Forget(obj)
		utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
		return true
	}

	// Run syncHandler, passing it the namespace/name key of the resource to
	// be synced
	err := c.processItem(key)
	// handleErr will handle adding to the queue again if retries are not
	// exceeded. If there is no error, it will forget the key.
	c.handleErr(err, key)

	return true
}

func (c *Controller) processItem(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Check if it's something without a namespace. If it is, it must be a Node.
	if len(namespace) == 0 {
		// Currently nodes are the only thing without a namespace listened for.
		node, err := c.nodeLister.Get(name)
		if err != nil {
			// The Node  may no longer exist, in which case we stop processing.
			if errors.IsNotFound(err) {
				utilruntime.HandleError(fmt.Errorf("node '%s' in work queue no longer exists", key))
				return nil
			}
			return err
		}

		// Pass the node to processNode
		err = c.processNode(node)
		return err
	}

	// It has a namespace, it must be a pod.
	pod, err := c.podLister.Pods(namespace).Get(name)
	if err != nil {
		// The pod  may no longer exist, in which case we stop processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("pod '%s' in work queue no longer exists", key))
			return nil
		}
		return err
	}

	// Pass the pod to processPod
	err = c.processPod(pod)

	return err
}

// processPod implements the logic for how to react to observing a pod in a
// given state. It currently copies the files passed in at runtime to any pod
// with a waiting initcontainer, and returns errors status updates and logs via
// the pod's ErrChan that is stored in c.podAliveMap.
func (c *Controller) processPod(pod *corev1.Pod) error {
	c.Debug("processPod called", "pod", pod.ObjectMeta.Name)
	// Assume only 1 init container Given we control this (Deploy() / Spawn()
	// this is ok)
	if len(pod.Status.InitContainerStatuses) != 0 {
		switch {
		case pod.Status.InitContainerStatuses[0].State.Waiting != nil:
			c.Debug("init container waiting", "pod", pod.ObjectMeta.Name)
		case pod.Status.InitContainerStatuses[0].State.Running != nil:
			c.Debug("init container running", "pod", pod.ObjectMeta.Name)
			c.Debug("calling CopyTar", "pod", pod.ObjectMeta.Name, "files", c.opts.Files)
			err := c.libclient.CopyTar(c.opts.Files, pod)
			// If this errors the controller will not die. It will just be
			// logged.
			if err != nil {
				c.sendErrChan(fmt.Sprintf("CopyTar for pod %s failed: %s", pod.ObjectMeta.Name, err))
				return err
			}
		}
	}
	// Error handling
	if pod.Status.Phase == corev1.PodSucceeded {
		c.Debug("pod exited succesfully, notifying", "pod", pod.ObjectMeta.Name)
		// Get the pod's errChan, return nil signifying that the pod
		// (runner) exited succesfully.
		result, ok := c.podAliveMap.Load(pod.ObjectMeta.UID)
		if ok {
			go func() {
				req := result.(*PodAlive)
				if !req.Done {
					req.Done = true
					c.Info("sending nil on error chan", "pod", pod.ObjectMeta.Name)
					req.ErrChan <- nil
					close(req.ErrChan)
				}
			}()
		} else {
			c.Error("could not find return error channel", "pod", pod.ObjectMeta.Name)
			return fmt.Errorf("Could not find return error channel for pod %s", pod.ObjectMeta.Name)
		}
	}
	// Entire pod has failed for some reason.
	if pod.Status.Phase == corev1.PodFailed {
		result, ok := c.podAliveMap.Load(pod.ObjectMeta.UID)
		if ok {
			go func() {
				req := result.(*PodAlive)
				if !req.Done {
					req.Done = true
					c.Error("sending error on error chan", "pod", pod.ObjectMeta.Name, "error", "pod failed")
					req.ErrChan <- fmt.Errorf("Pod %s failed", pod.ObjectMeta.Name)
					close(req.ErrChan)
				}
			}()
		} else {
			c.Error("could not find return error channel", "pod", pod.ObjectMeta.Name)
			return fmt.Errorf("Could not find return error channel for pod %s", pod.ObjectMeta.Name)
		}
		// Get logs
		logs, err := c.libclient.GetLog(pod, 25)
		if err != nil {
			c.Error("failed to get logs", "pod", pod.ObjectMeta.Name, "err", err)
		}
		// Callback to user, trying to give an informative error.
		c.Error("Pod failed", "pod", pod.ObjectMeta.Name, "message", pod.Status.Message, "reason", pod.Status.Reason)
		c.sendErrChan(fmt.Sprintf("Pod %s failed. Reason: %s, Message: %s\n Logs: %s", pod.ObjectMeta.Name, pod.Status.Message, pod.Status.Reason, logs))
		c.sendBadServer(&cloud.Server{
			Name: pod.ObjectMeta.Name,
		})
	}
	// Pod is pending (cluster has not got enough capacity.)
	if pod.Status.Phase == corev1.PodPending {
		// If the request on the container is currently not possible, send a
		// callback to the user
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Reason == "Unschedulable" {
				c.sendErrChan(fmt.Sprintf("Pod %s pending, reason: %s", pod.ObjectMeta.Name, condition.Message))
			}
		}
	}
	// This is commented out as It ~should~ not be needed. If the wr
	// functionality changes in the future, it may be useful. It checks the last
	// termination state, which is only relevant when the container restart
	// policy !never. It Handles individual container failures.

	// if len(pod.Status.ContainerStatuses) != 0 {
	//  switch {
	//  case pod.Status.ContainerStatuses[0].LastTerminationState.Terminated != nil:
	//      // Get logs
	//      logs, err := c.libclient.GetLog(pod, 25)
	//      if err != nil {
	//          c.Error(fmt.Sprintf("Failed to get logs for pod %s", pod.ObjectMeta.Name), "err", err)
	//      }
	//      c.sendErrChan(fmt.Sprintf("Pod %s container terminated. Reason: %s, Exit code: %v\n %s",
	//          pod.ObjectMeta.Name,
	//          pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.Reason,
	//          pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.ExitCode,
	//          logs))
	//      c.Info(fmt.Sprintf("Pod %s container terminated. Reason: %s, Exit code: %v",
	//          pod.ObjectMeta.Name,
	//          pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.Reason,
	//          pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.ExitCode))
	//  }
	// }
	return nil
}

// processNode is called whenever an informer encounters a changed node.
func (c *Controller) processNode(node *corev1.Node) error {
	// Keep a running total of the total allocatable resources. Adds information
	// to a map[nodeName]resourcelist.
	c.Debug("adding node to resource map", "node", node.ObjectMeta.Name, "resources", node.Status.Allocatable)
	c.Debug("obtaining resourcemutex Lock()")
	c.nodeResourceMutex.Lock()
	c.Debug("obtained resourcemutex Lock()")
	// Set the allocatable resource amount
	c.nodeResources[nodeName(node.ObjectMeta.Name)] = node.Status.Allocatable
	c.nodeResourceMutex.Unlock()
	c.Debug("returned resourcemutex Lock()")
	return nil
}

// Nice unified error handling
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.workqueue.Forget(key)
		return
	}

	if c.workqueue.NumRequeues(key) < maxRetries {
		c.Error("error processing key", "key", key, "error", err.Error())
		c.sendErrChan(fmt.Sprintf("Error processing key %v: %v", key, err.Error()))
		c.workqueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	c.Error("dropping key out of queue", "key", key, "error", err.Error())
	c.sendErrChan(fmt.Sprintf("Dropping key %q out of queue %v", key, err.Error()))
	c.workqueue.Forget(key)
}

// Send a string of the provided error to the callback channel
func (c *Controller) sendErrChan(err string) {
	c.Debug("sendErrChan called", "error", err)
	go func() {
		c.opts.CbChan <- err
	}()
}

// Send a *cloud.Server to wr that's gone bad.
func (c *Controller) sendBadServer(server *cloud.Server) {
	c.Debug("sendBadServer called")
	go func() {
		c.opts.BadCbChan <- server
	}()
}

func (c *Controller) runReqCheck() {
	c.Debug("runReqCheck() called")
	// If nodeResources is not initialised, wait.
	if c.nodeResourcesLen == nil || *c.nodeResourcesLen == 0 {
		c.Debug("node resources not initialised. Waiting")
		c.Debug("obtaining RLock()")
		c.nodeResourceMutex.RLock()
		c.Debug("obtained RLock()")
		c.nodeResourcesLen = func(i int) *int { return &i }(len(c.nodeResources))
		c.nodeResourceMutex.RUnlock()
		c.Debug("returned RLock()")
	} else if *c.nodeResourcesLen != 0 {
		for c.reqCheckHandler() {
			c.Debug("inside loop whilst reqCheckHandler is true")
		}
	}

	c.Debug("runReqCheck() exiting")
}

// For now ignore that you can set quotas on a k8s cluster. Assume that the user
// can schedule an entire node. Not using PV's at all here. Currently there is
// no way to catastrophically fail, if that were to happen, this func should
// return falses ToDO: Multiple PV/Not PV types.
func (c *Controller) reqCheckHandler() bool {
	c.Debug("reqCheckHandler() called.")
	req := <-c.opts.ReqChan
	c.Debug("reqCheckHandler() recieved req.")
	c.Debug("obtaining RLock()")
	c.nodeResourceMutex.RLock()
	c.Debug("obtained RLock()")

	defer c.nodeResourceMutex.RUnlock()
	defer c.Debug("returned RLock()")

	StorageEphemeralEnabled := false

	// Check if any node reports ephemeral storage
	for _, n := range c.nodeResources {
		if _, ok := n["ephemeral-storage"]; ok {
			StorageEphemeralEnabled = true
		}
	}

	// If no node reports ephemeral storage, turn off the check. Disk requests
	// will silently ignored
	if !StorageEphemeralEnabled {
		for _, n := range c.nodeResources {
			// A Node should always have equal or more resource than the
			// request. (The  .Cmp function works:  1 = '>', 0 = '==', -1 =
			// '<'), return when the first node that meets the requirements is
			// found.
			if n.Cpu().Cmp(req.Cores) != -1 && n.Memory().Cmp(req.RAM) != -1 {
				c.Debug("returning schedulable from reqCheckHandler", "req", req)
				req.CbChan <- Response{
					Error:     nil, // It is possible to eventually schedule
					Ephemeral: StorageEphemeralEnabled,
				}

				// If disk was requested, warn.
				if resource.NewQuantity(int64(0), resource.DecimalSI).Cmp(req.Disk) != 0 {
					c.sendErrChan(`The cluster is not reporting node ephemeral storage. 
				If your command requires more storage than is avaliable on the node or there are competing requests it may fail.`)
				}

				return true
			}
		}
	} else { // Otherwise turn it on
		for _, n := range c.nodeResources {
			// A Node should always have equal or more resource than the
			// request. (The  .Cmp function works:  1 = '>', 0 = '==', -1 =
			// '<'), return when the first node that meets the requirements is
			// found.
			if n.Cpu().Cmp(req.Cores) != -1 &&
				n.StorageEphemeral().Cmp(req.Disk) != -1 &&
				n.Memory().Cmp(req.RAM) != -1 {
				c.Debug("returning schedulable from reqCheckHandler", "req", req)
				req.CbChan <- Response{
					Error:     nil, // It is possible to eventually schedule
					Ephemeral: StorageEphemeralEnabled,
				}

				return true
			}
		}
	}

	c.Error("reqCheck failed. No node has capacity for request", "req", req)
	req.CbChan <- Response{
		Error:     fmt.Errorf("No node has the capacity to schedule the current job"),
		Ephemeral: StorageEphemeralEnabled}
	c.sendErrChan(fmt.Sprintf("No node has the capacity to schedule the current job"))

	return true
}

func (c *Controller) runPodAlive() {
	c.Debug("runPodAlive() called")
	for c.podAliveHandler() {
		c.Debug("podAliveHandler() returned true")
	}
	c.Debug("runPodAlive() exiting")
}

// podAliveHandler recieves a request and adds the channel in that request to
// the podAliveMap with the key being the UID of the pod.
func (c *Controller) podAliveHandler() bool {
	c.Debug("podAliveHandler() called")
	req := <-c.opts.PodAliveChan
	c.Debug("recieved PodAlive Request", "pod", req.Pod.ObjectMeta.Name)
	// Store the error channel in the map.
	c.podAliveMap.Store(req.Pod.ObjectMeta.UID, req)
	c.Debug("Stored alive request for pod in map, returning true", "pod", req.Pod.ObjectMeta.Name)

	return true
}
