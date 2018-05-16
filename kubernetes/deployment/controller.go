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

package deployment

/*
Package deployment a kubernetes controller to oversee the deployment
of the wr scheduler controller into a kubernetes cluster. It handles
copying configuration files and binaries as well as port forwarding.
*/

import (
	"fmt"
	"time"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const maxRetries = 5

type Controller struct {
	Client     *client.Kubernetesp
	Clientset  kubernetes.Interface
	Restconfig *rest.Config
	Opts       *DeployOpts
	queue      workqueue.RateLimitingInterface
	informer   cache.SharedIndexInformer
}

type DeployOpts struct {
	ContainerImage  string             // docker hub image
	TempMountPath   string             // where to mount the binary
	Files           []client.FilePair  // Files to tar across
	BinaryPath      string             // full path to the binary to be executed
	BinaryArgs      []string           // arguments to the binary
	ConfigMapName   string             // name of the configmap to execute
	ConfigMountPath string             // path to the configmap
	RequiredPorts   []int              // ports that require forwarding
	AttachCmdOpts   *client.CmdOptions // relevant options for the attachCmd
}

func (c *Controller) createQueueAndInformer() {
	c.queue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	c.informer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return c.Clientset.CoreV1().Pods(c.Client.NewNamespaceName).List(metav1.ListOptions{
					LabelSelector: "app=wr-manager",
				})

			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return c.Clientset.CoreV1().Pods(c.Client.NewNamespaceName).Watch(metav1.ListOptions{
					LabelSelector:        "app=wr-manager",
					IncludeUninitialized: true,
					Watch:                true,
				})
			},
		},
		&apiv1.Pod{},
		0, //Skip resync
		cache.Indexers{},
	)
}

func (c *Controller) addEventHandlers() {
	c.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				c.queue.Add(key)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(newObj)
			if err == nil {
				c.queue.Add(key)
			}

		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				c.queue.Add(key)
			}
		},
	})
}

// HasSynced is required for cache.Controller interface.
func (c *Controller) HasSynced() bool {
	return c.informer.HasSynced()
}

// Run starts SharedInformer watching for pods, and sends their keys to workqueue
// StopCh used to send interrupt
func (c *Controller) Run(stopCh <-chan struct{}) {
	c.createQueueAndInformer()
	c.addEventHandlers()
	// don't let panics crash the process
	defer utilruntime.HandleCrash()
	// Ensure workqueue is shut down properly
	defer c.queue.ShutDown()

	go c.informer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	// Before starting, create the initial deployment
	err := c.Client.Deploy(c.Opts.ContainerImage, c.Opts.TempMountPath, c.Opts.Files, c.Opts.BinaryPath, c.Opts.BinaryArgs, c.Opts.ConfigMapName, c.Opts.ConfigMountPath, c.Opts.RequiredPorts)
	if err != nil {
		panic(err)
	}
	// runWorker loops until 'bad thing'. '.Until' will
	// restart the worker after a second
	wait.Until(c.runWorker, time.Second, stopCh)
}

func (c *Controller) runWorker() {
	// processNextWorkItem automatically waits for work
	for c.processNextItem() {
		// loop
	}
}

func (c *Controller) processNextItem() bool {
	// pull next key from queue.
	// look up key in cache
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	// Indicate to queue key has been processed
	defer c.queue.Done(key)

	// do processing on key
	err := c.processItem(key.(string))

	if err == nil {
		// No error => queue stop tracking history
		c.queue.Forget(key)
	} else if c.queue.NumRequeues(key) < maxRetries {
		fmt.Printf("Error processing %s, will retry: %v\n", key, err)
		// requeue
		c.queue.AddRateLimited(key)
	} else {
		// err != nil and too many retries
		fmt.Printf("Error processing %s, giving up: %v\n", key, err)
		c.queue.Forget(key)
		utilruntime.HandleError(err)
	}

	return true
}

// processItem(key) is where we define how to react to a pod event
func (c *Controller) processItem(key string) error {
	fmt.Printf("Processing change t Pod %s\n", key)

	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil {
		return fmt.Errorf("Error fetching object with key %s from store: %v", key, err)
	}

	if !exists {
		fmt.Printf("Object with key %s deleted. \n\nObj: %v", key, obj)
		fmt.Printf("\n\n")
		fmt.Println("====================")
		fmt.Printf("\n\n")
		return nil
	}
	c.processObj(obj)
	//jsonObj, err := json.Marshal(obj)
	//fmt.Printf(string(jsonObj))
	//fmt.Printf("Object with key %s created. \n\nObj: %v", key, obj)
	fmt.Printf("\n\n")
	fmt.Println("====================")
	fmt.Printf("\n\n")
	return nil
}

func (c *Controller) processObj(obj interface{}) error {
	fmt.Println("processObj called")
	fmt.Printf("Object has type %T\n", obj)
	switch v := obj.(type) {
	case *apiv1.Pod:
		fmt.Println("Case pod. Calling processPod")
		c.processPod(v)
	default:
		fmt.Println("Default case executed, throwing error")
		return error(fmt.Errorf("Error: obj is not a pod."))
	}
	return nil
}

// Assume there is only 1 initcontainer
func (c *Controller) processPod(obj *apiv1.Pod) {
	fmt.Println("processPod Called")
	if len(obj.Status.InitContainerStatuses) != 0 {
		switch {
		case obj.Status.InitContainerStatuses[0].State.Waiting != nil:
			fmt.Println("InitContainer Waiting!")
		case obj.Status.InitContainerStatuses[0].State.Running != nil:
			fmt.Println("InitContainer Running!")
			fmt.Println("Calling CopyTar")
			// here goes nothing
			c.Client.CopyTar(c.Opts.Files, obj)
		case obj.Status.ContainerStatuses[0].State.Running != nil:
			fmt.Println("WR manager container is running, calling PortForward")
			go c.Client.PortForward(obj, c.Opts.RequiredPorts)
		default:
			fmt.Println("Not InitContainer or WR Manager container related")
		}
	} else {
		fmt.Println("InitContainerStatuses not initialised yet")
	}
	return
}
