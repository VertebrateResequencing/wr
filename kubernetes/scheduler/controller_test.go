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

package scheduler_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubescheduler "github.com/VertebrateResequencing/wr/kubernetes/scheduler"
	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/inconshreveable/log15"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
)

var lc *client.Kubernetesp
var autherr error
var testingNamespace string
var callBackChan chan string
var badCallBackChan chan *cloud.Server
var reqChan chan *kubescheduler.Request
var podAliveChan chan *kubescheduler.PodAlive
var clientset kubernetes.Interface
var restConfig *rest.Config

func init() {

	lc = &client.Kubernetesp{}

	clientset, restConfig, autherr = lc.Authenticate()
	if autherr != nil {
		panic(autherr)
	}

	rand.Seed(time.Now().UnixNano())
	testingNamespace = strings.Replace(namesgenerator.GetRandomName(1), "_", "-", -1) + "-wr-testing"

	_ = lc.CreateNewNamespace(testingNamespace)

	autherr = lc.Initialize(clientset, testingNamespace)
	if autherr != nil {
		panic(autherr)
	}

	// Set up message notifier & request channels
	callBackChan = make(chan string, 5)
	badCallBackChan = make(chan *cloud.Server, 5)
	reqChan = make(chan *kubescheduler.Request)
	podAliveChan = make(chan *kubescheduler.PodAlive)

	// Initialise the informer factory
	// Confine all informers to the provided namespace
	kubeInformerFactory := kubeinformers.NewFilteredSharedInformerFactory(clientset, time.Second*15, testingNamespace, func(listopts *metav1.ListOptions) {
		listopts.IncludeUninitialized = true
	})

	// Use certificate files from the deployment controller tests
	caFile := "/tmp/ca.pem"
	certFile := "/tmp/cert.pem"
	keyFile := "/tmp/key.pem"
	wrDir := "/wr-tmp/.wr_production/"
	wrLogDir := "/tmp/"

	opts := kubescheduler.ScheduleOpts{
		Files:        []client.FilePair{{"/tmp/wr", "/wr-tmp/"}, {caFile, wrDir}, {certFile, wrDir}, {keyFile, wrDir}},
		CbChan:       callBackChan,
		BadCbChan:    badCallBackChan,
		ReqChan:      reqChan,
		PodAliveChan: podAliveChan,
		Logger:       log15.New(),
		ManagerDir:   wrLogDir,
	}

	// Create the controller
	controller := kubescheduler.NewController(clientset, restConfig, lc, kubeInformerFactory, opts)
	stopCh := make(chan struct{})

	go kubeInformerFactory.Start(stopCh)

	// Start the scheduling controller
	go func() {
		if err := controller.Run(2, stopCh); err != nil {
			panic(fmt.Errorf("Controller failed: %s", err))
		}
	}()
}

// This test tests that the nodes are correctly being reported,
// and that Resource requests are being handled correctly.TestReqCheck
// In the testing environment (Travis CI the VM has 2 vcpus and ~7gb ram)
func TestReqCheck(t *testing.T) {
	// Resources are limited in CI.
	passCases := []struct {
		cores int
		ram   int
		disk  int
	}{
		{
			cores: 1,
			ram:   2,
			disk:  0,
		},
		{
			cores: 0,
			ram:   0,
			disk:  0,
		},
		{
			cores: 0,
			ram:   1,
			disk:  0,
		},
		{
			cores: 1,
			ram:   0,
			disk:  0,
		},
	}
	for _, c := range passCases {
		// Rewrite *Requirements to a kubescheduler.Request
		cores := resource.NewMilliQuantity(int64(c.cores)*1000, resource.DecimalSI)
		ram := resource.NewQuantity(int64(c.ram)*1024*1024, resource.BinarySI)
		disk := resource.NewQuantity(int64(c.disk)*1024*1024*1024, resource.BinarySI)

		// Create the request.
		r := &kubescheduler.Request{
			RAM:    *ram,
			Time:   5 * time.Second,
			Cores:  *cores,
			Disk:   *disk,
			Other:  map[string]string{},
			CbChan: make(chan kubescheduler.Response),
		}

		// Send and wait
		go func() {
			reqChan <- r
		}()

		resp := <-r.CbChan

		if resp.Error != nil {
			t.Errorf("A test that should've passed errored: %s with case %+v", resp.Error, c)
		}

		// We are testing on 1.10, so ephemeral should always return true
		if !resp.Ephemeral {
			t.Errorf("Ephemeral check failed, should return true. (Are you running kubernetes 1.10+?)")
		}
	}

	// Assume the CI VM won't have 42 cores, ram or disk.
	failCases := []struct {
		cores int
		ram   int
		disk  int
	}{
		{
			cores: 9999999,
			ram:   9999999,
			disk:  9999999,
		},
	}
	for _, c := range failCases {
		// Rewrite *Requirements to a kubescheduler.Request
		cores := resource.NewMilliQuantity(int64(c.cores)*1000, resource.DecimalSI)
		ram := resource.NewQuantity(int64(c.ram)*1024*1024, resource.BinarySI)
		disk := resource.NewQuantity(int64(c.disk)*1024*1024*1024, resource.BinarySI)

		r := &kubescheduler.Request{
			RAM:    *ram,
			Time:   5 * time.Second,
			Cores:  *cores,
			Disk:   *disk,
			Other:  map[string]string{},
			CbChan: make(chan kubescheduler.Response),
		}

		go func() {
			reqChan <- r
		}()

		resp := <-r.CbChan

		if resp.Error == nil {
			t.Errorf("A test that should've failed passed, case: %+v", c)
		}

		// We are testing on 1.10, so ephemeral should always return true
		if !resp.Ephemeral {
			t.Errorf("Ephemeral check failed, should return true. (Are you running kubernetes 1.10+?)")
		}
	}
}

// This test is really to test reporting of why
// something might've blown up.
func TestRunCmd(t *testing.T) {
	passCases := []struct {
		resourceReq   apiv1.ResourceRequirements
		configMapData string
	}{
		{
			resourceReq: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					apiv1.ResourceCPU:              *resource.NewMilliQuantity(int64(1)*100, resource.DecimalSI),
					apiv1.ResourceMemory:           *resource.NewQuantity(int64(250)*1024*1024, resource.BinarySI),
					apiv1.ResourceEphemeralStorage: *resource.NewQuantity(int64(0)*1024*1024*1024, resource.BinarySI),
				},
				Limits: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewMilliQuantity(int64(1)*100, resource.DecimalSI),
					apiv1.ResourceMemory: *resource.NewQuantity(int64(250)*1024*1024, resource.BinarySI),
				},
			},
			configMapData: " ",
		},
	}
	for _, c := range passCases {
		configMountPath := "/scripts"
		cmd := []string{"echo testing runcmd"}

		configmap, err := lc.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
		}

		pod, err := lc.Spawn("ubuntu:latest",
			"/wr-tmp/",
			configMountPath+"/"+client.DefaultScriptName,
			cmd,
			configmap.ObjectMeta.Name,
			configMountPath,
			c.resourceReq)

		if err != nil {
			t.Errorf("Spawn failed: %s", err)
		}

		// Send the request to the listener.
		errChan := make(chan error)
		go func() {
			req := &kubescheduler.PodAlive{
				Pod:     pod,
				ErrChan: errChan,
				Done:    false,
			}
			podAliveChan <- req
		}()

		// Wait on the error.
		err = <-errChan
		if err != nil {
			t.Errorf("errChan recived error: %s", err)
		}

	}
	// This fail case emulates a configmap (cloud script)
	// failing. (call /bin/false)
	failCases := []struct {
		resourceReq   apiv1.ResourceRequirements
		configMapData string
	}{
		{
			resourceReq: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					apiv1.ResourceCPU:              *resource.NewMilliQuantity(int64(1)*100, resource.DecimalSI),
					apiv1.ResourceMemory:           *resource.NewQuantity(int64(250)*1024*1024, resource.BinarySI),
					apiv1.ResourceEphemeralStorage: *resource.NewQuantity(int64(0)*1024*1024*1024, resource.BinarySI),
				},
				Limits: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewMilliQuantity(int64(1)*100, resource.DecimalSI),
					apiv1.ResourceMemory: *resource.NewQuantity(int64(250)*1024*1024, resource.BinarySI),
				},
			},
			configMapData: "/bin/false",
		},
	}
	for _, c := range failCases {
		configMountPath := "/scripts"
		cmd := []string{"echo testing runcmd fails"}

		configmap, err := lc.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
		}

		pod, err := lc.Spawn("ubuntu:latest",
			"/wr-tmp/",
			configMountPath+"/"+client.DefaultScriptName,
			cmd,
			configmap.ObjectMeta.Name,
			configMountPath,
			c.resourceReq)

		if err != nil {
			t.Errorf("Spawn failed: %s", err)
		}

		// Send the request to the listener.
		errChan := make(chan error)
		go func() {
			req := &kubescheduler.PodAlive{
				Pod:     pod,
				ErrChan: errChan,
				Done:    false,
			}
			podAliveChan <- req
		}()

		err = <-errChan
		if err == nil {
			t.Errorf("errChan recived no error")
		}
		t.Logf("errChan recieved error: %s", err)

	}
}
