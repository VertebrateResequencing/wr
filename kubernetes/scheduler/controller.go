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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	"k8s.io/client-go/util/workqueue"
)

const (
	// maxRetries is the number of times a deployment will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a work item is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

// Controller stores everything the controller needs
type Controller struct {
	libclient     *client.Kubernetesp
	kubeclientset kubernetes.Interface
	restconfig    *rest.Config
	podLister     corelisters.PodLister
	podSynced     cache.InformerSynced
	nodeLister    corelisters.NodeLister
	nodeSynced    cache.InformerSynced
	workqueue     workqueue.RateLimitingInterface
	opts          ScheduleOpts
	nodeResources map[nodeName]corev1.ResourceList
	podAliveMap   *sync.Map
}

// ScheduleOpts stores options  for the scheduler
type ScheduleOpts struct {
	Files        []client.FilePair // files to copy to each spawned runner. Potentially listen on a channel later.
	CbChan       chan string       // Channel to send errors on
	ReqChan      chan *Request     // Channel to send requests about resource availability to
	PodAliveChan chan *PodAlive
}

// Request contains relevant information
// for processing a request.
type Request struct {
	RAM    *resource.Quantity
	Time   time.Duration
	Cores  *resource.Quantity
	Disk   *resource.Quantity
	Other  map[string]string
	CbChan chan error
}

// PodAlive contains a pod, and a chan error
// that is closed when the pod terminates succesfully.
type PodAlive struct {
	Pod     *corev1.Pod
	ErrChan chan error
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
		opts:          opts,
		nodeResources: make(map[nodeName]corev1.ResourceList),
	}

	// Set up event handlers
	// Only watch pods with the label 'app=wr-runner'
	podInformer.Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				pod := obj.(*corev1.Pod)
				return pod.ObjectMeta.Labels["app=wr-runner"] == "true"
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: controller.podHandler,
				UpdateFunc: func(old, new interface{}) {
					newPod := new.(*corev1.Pod)
					oldPod := old.(*corev1.Pod)
					if newPod.ResourceVersion == oldPod.ResourceVersion {
						// Periodic resync will send update events for all known pods
						// if they're different they will have different RVs
						return
					}
					controller.podHandler(new)
				},
				DeleteFunc: controller.podHandler, // remove node from nodeResources
			},
		})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.nodeHandler,
		UpdateFunc: func(old, new interface{}) {
			newNode := new.(*corev1.Node)
			oldNode := old.(*corev1.Node)
			if newNode.ResourceVersion == oldNode.ResourceVersion {
				return
			}
			controller.nodeHandler(new)
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("Couldn't cast object to node for object %#v", obj))
				return
			}
			// Delete the node from the nodeResources map.
			// Do it here, so it's not an item added to the queue.
			// Therefore deletions potentially are reacted to more quickly
			delete(controller.nodeResources, nodeName(node.ObjectMeta.Name))
		},
	})

	// Set up the map[pod.ObjectMeta.UID]errChan
	// where errChan is a unique channel for handling errors
	// related to the passed pod. Uses the sync.Map as
	// it fits use case 1 exactly: "The Map type is optimized for
	// two common use cases: (1) when the entry for a given key
	// is only ever written once but read many times.."
	controller.podAliveMap = new(sync.Map)

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

	// start request handler(s)
	c.handleReqCheck(2)
	c.handlePodAlive(2)

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
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}

		// Run syncHandler, passing it the namespace/name key of the resource to be synced
		err := c.processItem(key)
		// handleErr will handle adding to the queue again.
		c.handleErr(err, key)
		// Finally, if no error occurs forget the item so it's not queued again
		c.workqueue.Forget(obj)
		return nil

	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}
	return true
}

func (c *Controller) processItem(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Check if it's something without a namespace.
	// If it is, it must be a Node.
	if len(namespace) == 0 {

		// Currently nodes are the only thing without a namespace listened for.
		node, err := c.nodeLister.Get(name)

		if err != nil {
			// The Node  may no longer exist, in which case we stop
			// processing.

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
		// The pod  may no longer exist, in which case we stop
		// processing.
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

// podHandler adds pods to the queue
// may be expanded later, generally there are separate
// add, update and delete functions. Currently this
// isn't needed as we are just watching for when to call
// CopyTar()
func (c *Controller) podHandler(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't cast object to pod for object %#v", obj))
		return
	}
	key, err := controller.KeyFunc(pod)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", pod, err))
		return
	}
	c.workqueue.Add(key)
}

// nodeHandler adds nodes to the queue
// may be expanded later, generally there are separate
// add, update and delete functions. Currently this
// isn't needed as we just want a running total of allocatable
// resources
func (c *Controller) nodeHandler(obj interface{}) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't cast object to node for object %#v", obj))
		return
	}
	key, err := controller.KeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", node, err))
		return
	}
	c.workqueue.Add(key)
}

// Assume there is only 1 initcontainer
// Copy tar to waiting initcontainers.
func (c *Controller) processPod(pod *corev1.Pod) error {
	if len(pod.Status.InitContainerStatuses) != 0 {
		switch {
		case pod.Status.InitContainerStatuses[0].State.Waiting != nil:
			fmt.Println("InitContainer Waiting!")
		case pod.Status.InitContainerStatuses[0].State.Running != nil:
			fmt.Println("InitContainer Running!")
			fmt.Println("Calling CopyTar")
			err := c.libclient.CopyTar(c.opts.Files, pod)
			if err != nil {
				return err
			}
		default:
		}
	} else if pod.Status.Phase == corev1.PodPhase("Succeeded") {
		// Get the pod's errChan
		result, ok := c.podAliveMap.Load(pod.ObjectMeta.UID)
		if ok {
			ec := result.(chan error)
			ec <- nil
		} else {
			return fmt.Errorf("Could not find return error channel for pod %s", pod.ObjectMeta.Name)
		}
	}
	return nil
}

// Keep a running total of the total allocatable resources.
// Adds information to a map[nodeName]resourcelist.
func (c *Controller) processNode(node *corev1.Node) error {
	// Set the capacity. May want allocatable later ?
	// log me!
	// This shouldn't need to be a threadsafe map, as processNode
	// will only be called by processItem provided a key from the
	// workqueue
	c.nodeResources[nodeName(node.ObjectMeta.Name)] = node.Status.Allocatable
	return nil
}

func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.workqueue.Forget(key)
		return
	}

	if c.workqueue.NumRequeues(key) < maxRetries {
		//glog.V(2).Infof("Error processing key %v: %v", key, err)
		c.sendErrChan(fmt.Sprintf("Error processing key %v: %v", key, err.Error()))
		c.workqueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	//glog.V(2).Infof("Dropping key %q out of the queue: %v", key, err)
	c.sendErrChan(fmt.Sprintf("Dropping key %q out of queue %v", key, err.Error()))
	c.workqueue.Forget(key)
}

// Send a string of the provided error to the callback channel
func (c *Controller) sendErrChan(err string) {
	c.opts.CbChan <- err
}

func (c *Controller) handleReqCheck(threadiness int) {
	for i := 0; i < threadiness; i++ {
		go c.reqCheckHandler()
	}
}

// For now ignore that you can set quotas on a k8s cluster.
// Assume that the user can schedule an entire node.
// Not using PV's at all here.
// ToDO: Multiple PV/Not PV types.
func (c *Controller) reqCheckHandler() {
	for {
		req := <-c.opts.ReqChan
		for _, n := range c.nodeResources {
			if req.Cores.Cmp(n[corev1.ResourceName("cpu")]) != 1 &&
				req.Disk.Cmp(n[corev1.ResourceName("ephemeral-storage")]) != 1 &&
				req.RAM.Cmp(n[corev1.ResourceName("memory")]) != 1 {
				req.CbChan <- nil // It is possible to eventually schedule
			}
		}
		req.CbChan <- fmt.Errorf("No node has the capacity to schedule the current job")
	}
}

func (c *Controller) handlePodAlive(threadiness int) {
	for i := 0; i < threadiness; i++ {
		go c.podAliveHandler()
	}
}

// podAliveHandler recieves a request and adds the channel
// in that request to the podAliveMap with the key being
// the UID of the pod.
func (c *Controller) podAliveHandler() {
	for {
		req := <-c.opts.PodAliveChan
		// Store the error channel in the map.
		c.podAliveMap.Store(req.Pod.ObjectMeta.UID, req.ErrChan)
		// log req recieved and processed.
	}
}
