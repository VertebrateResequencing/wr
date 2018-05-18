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
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubescheduler "github.com/VertebrateResequencing/wr/kubernetes/scheduler"
	"github.com/inconshreveable/log15"
	kubeinformers "k8s.io/client-go/informers"
	"time"
)

// Kubernetes is the implementer of scheduleri.
// it is a wrapper to implement scheduleri by sending requests to the controller.
// all of the logic to determine how / if a cmd can be scheduled is taken care of inside the controller.
// this reduces communication between the two pieces of code.

// maxQueueTime(), reserveTimeout(), hostToID()
// are inherited from local
type k8s struct {
	local
	log15.Logger
	callBackChan chan string
	requestChan  chan request
}

type request struct {
	job job
	ack chan struct{}
	err error
}

// Set up prerequisites, call Run()
// Create channels to pass requests to the controller.
func (s *k8s) initialize(namespace string, logger log15.Logger) error {
	s.Logger = logger.New("scheduler", "kubernetes")
	// Set up message notifier
	s.callBackChan = make(chan string, 5)
	go notifyMessage(s.callBackChan)
	// Prerequisites to start the controller
	libclient := &client.Kubernetesp{}
	kubeClient, restConfig, err := libclient.Authenticate() // Authenticate against the cluster.
	if err != nil {
		return err
	}
	// Initialise all internal clients on  the provided namespace
	libclient.Initialize(kubeClient, namespace)
	// Initialise the informer factory
	kubeInformerFactory := kubeinformers.NewFilteredSharedInformerFactory(kubeClient, time.Second*30, namespace)

	// Create the controller
	controller := kubescheduler.NewController(kubeClient, restConfig, libclient, kubeInformerFactory)

	go kubeInformerFactory.Start(stopCh)

	// Start the scheduling controller
	if err = controller.Run(2, stopCh); err != nil {
		logger.Error("Error running controller", err.Error())
	}

	return nil
}

// creates a schedule request to send to the controller.
func (s *k8s) schedule(cmd string, req *Requirements, count int) error {
	return nil
}

// creates a busy request to send to the controller.
func (s *k8s) busy() bool {
	return nil
}

// setMessageCallBack sets the given callback.
func (s *k8s) setMessageCallback(cb MessageCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()
	s.msgCB = cb
}

// setBadServerCallBack sets the given callback.
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

	//TODO: Client-go code to delete namespace.
	//TODO: Do more elegantly later.

	return
}
