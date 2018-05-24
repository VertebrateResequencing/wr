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
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubescheduler "github.com/VertebrateResequencing/wr/kubernetes/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/inconshreveable/log15"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
)

// k8s is the implementer of scheduleri.
// it is a wrapper to implement scheduleri by sending requests to the controller

// maxQueueTime(), reserveTimeout(), hostToID(), busy(), schedule()
// are inherited from local
type k8s struct {
	local
	log15.Logger
	libclient       *client.Kubernetesp
	callBackChan    chan string
	cbmutex         sync.RWMutex
	badCallBackChan chan *cloud.Server
	reqChan         chan *kubescheduler.Request
	msgCB           MessageCallBack
	badServerCB     BadServerCallBack
}

// Set up prerequisites, call Run()
// Create channels to pass requests to the controller.
// Create queue.
func (s *k8s) initialize(namespace string, logger log15.Logger) error {
	s.Logger = logger.New("scheduler", "kubernetes")

	// make queue
	s.queue = queue.New(localPlace)

	// set our functions for use in schedule() and processQueue()
	s.reqCheckFunc = s.reqCheck
	s.canCountFunc = s.canCount
	s.runCmdFunc = s.runCmd
	s.cancelRunCmdFunc = s.cancelRun
	s.stateUpdateFunc = s.stateUpdate
	s.stateUpdateFreq = s.config.StateUpdateFrequency
	if s.stateUpdateFreq == 0 {
		s.stateUpdateFreq = 1 * time.Minute
	}

	// Set up message notifier & request channels
	s.callBackChan = make(chan string, 5)
	s.badCallBackChan = make(chan *cloud.Server, 5)
	s.reqChan = make(chan *kubescheduler.Request)

	// Prerequisites to start the controller
	s.libclient = &client.Kubernetesp{}
	kubeClient, restConfig, err := s.libclient.Authenticate() // Authenticate against the cluster.
	if err != nil {
		return err
	}

	// Initialise all internal clients on  the provided namespace
	s.libclient.Initialize(kubeClient, namespace)

	// Initialise the informer factory
	// Confine all informers to the provided namespace
	kubeInformerFactory := kubeinformers.NewFilteredSharedInformerFactory(kubeClient, time.Second*30, namespace, func(listopts *metav1.ListOptions) {
		listopts.IncludeUninitialized = true
		listopts.Watch = true
	})

	// Initialise scheduler opts
	opts := kubescheduler.ScheduleOpts{
		Files:   []client.FilePair{{"/tmp/foo.txt", "/tmp/bar.txt"}},
		CbChan:  s.callBackChan,
		ReqChan: s.reqChan,
	}

	// Start listening for messages on call back channels
	go s.notifyCallBack(s.callBackChan, s.badCallBackChan)

	// Create the controller
	controller := kubescheduler.NewController(kubeClient, restConfig, s.libclient, kubeInformerFactory, opts)

	stopCh := make(chan struct{})

	go kubeInformerFactory.Start(stopCh)

	// Start the scheduling controller
	go func() {
		if err = controller.Run(2, stopCh); err != nil {
			logger.Error("Error running controller", err.Error())
		}
	}()

	return nil
}

// Send a request to see if a cmd with the provided requirements
// can ever be scheduled.
// If the request can be scheduled, errChan returns nil then is closed
// If it can't ever be sheduled an error is sent on errChan and returned.
// TODO: OCC if error: What if a node is added shortly after? (Autoscaling?)
func (s *k8s) reqCheck(req *Requirements) error {
	// Create error channel
	errChan := make(chan error)
	// Rewrite *Requirements to a kubescheduler.Request
	cores := resource.NewMilliQuantity(int64(req.Cores)*1000, resource.DecimalSI)
	ram := resource.NewQuantity(int64(req.RAM)*1024*1024*1024, resource.BinarySI)
	disk := resource.NewQuantity(int64(req.Disk)*1000*1000*1000, resource.DecimalSI)
	r := &kubescheduler.Request{
		RAM:    ram,
		Time:   req.Time,
		Cores:  cores,
		Disk:   disk,
		Other:  req.Other,
		CbChan: errChan,
	}
	// Do i want this to be non blocking??
	// Do i want it to block in a goroutine??

	// Blocking sends are fine in a goroutine?
	go func() {
		s.reqChan <- r
	}()
	// select {
	// case s.reqChan <- r:
	// 	fmt.Println("Request sent")
	// default:
	// 	fmt.Println("No request sent")
	// }
	// Do i want this to block or not?
	// What about multiple errors?
	err := <-errChan

	return err
}

// setMessageCallBack sets the given callback function.
func (s *k8s) setMessageCallback(cb MessageCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()
	s.msgCB = cb
}

// setBadServerCallBack sets the given callback function.
func (s *k8s) setBadServerCallBack(cb BadServerCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()
	s.badServerCB = cb
}

// The controller is passed a callback channel.
// notifyMessage recieves on the channel
// if anything is recieved call s.msgCB(msg).
func (s *k8s) notifyCallBack(callBackChan chan string, badCallBackChan chan *cloud.Server) {
	for {
		select {
		case msg := <-callBackChan:
			go s.msgCB(msg)
		case badServer := <-badCallBackChan:
			go s.badServerCB(badServer)
		}
	}

}

// Delete the namespace when all pods have exited.
func (s *k8s) cleanup() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.cleaned = true
	err := s.queue.Destroy()
	if err != nil {
		s.Warn("cleanup queue destruction failed", "err", err)
	}

	err = s.libclient.TearDown()
	if err != nil {
		s.Warn("namespace deletion errored", "err", err)
	}
	return
}

// Work out how many pods with given resource requests can be scheduled based on resource requests on the
// nodes in the cluster.
func (s *k8s) canCount(req *Requirements) (canCount int) {

}

// RunFunc calls spawn() and exits with an error = nil when pod has terminated. (Runner exited)
// Or an error if there was a problem. Use deletefunc in controller to send message?
// (based on some sort of channel communication?)
func (s *k8s) runCmd(cmd string, req *Requirements, reservedCh chan bool) error {

}
