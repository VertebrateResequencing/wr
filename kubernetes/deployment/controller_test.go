// Copyright © 2018 Genome Research Limited
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

package deployment_test

import (
	"encoding/gob"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubedeployment "github.com/VertebrateResequencing/wr/kubernetes/deployment"
	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/inconshreveable/log15"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var dc kubedeployment.Controller
var autherr error
var nsErr error
var testingNamespace string
var skip bool
var dir string

// Just test that the call to Deploy() works, and that when configured as
// expected, the deployment controller will copy the tarball, and that the
// manager can be connected to.

func init() {
	dc = kubedeployment.Controller{
		Client: &client.Kubernetesp{},
	}
	dc.Clientset, dc.Restconfig, autherr = dc.Client.Authenticate(client.AuthConfig{})
	if autherr != nil {
		skip = true
		return
	}

	rand.Seed(time.Now().UnixNano())
	testingNamespace = strings.Replace(namesgenerator.GetRandomName(1), "_", "-", -1) + "-wr-testing"

	nsErr = dc.Client.CreateNewNamespace(testingNamespace)
	if nsErr != nil {
		fmt.Printf("Failed to create namespace: %s", nsErr)
		skip = true
		return
	}

	autherr = dc.Client.Initialize(dc.Clientset, testingNamespace)
	if autherr != nil {
		skip = true
		return
	}

	_, autherr := dc.Clientset.CoreV1().Endpoints(testingNamespace).List(metav1.ListOptions{})
	if autherr != nil {
		skip = true
		fmt.Printf("Failed to list endpoints for testing namespace, assuming cluster connection failure.\n Skipping tests with error: %s\n", autherr)
	}
}

func TestDeploy(t *testing.T) {
	if skip {
		t.Skip("skipping test; failed to access cluster")
	}
	cases := []struct {
		containerImage  string
		tempMountPath   string
		cmdArgs         []string
		configMountPath string
		configMapData   string
		requiredPorts   []int
	}{
		{
			containerImage:  "ubuntu:latest",
			tempMountPath:   "/wr-tmp/",
			cmdArgs:         []string{"/wr-tmp/wr", "manager", "start", "-f"},
			configMountPath: "/scripts/",
			configMapData:   "echo \"hello world\"",
			requiredPorts:   []int{8080, 8081},
		},
	}
	for _, c := range cases {

		// Test the creation of config maps (2 birds one stone). We won't delete
		// this now so we can use it later.
		configmap, err := dc.Client.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
		}

		expectedData := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"" +
			"\necho \"hello world\"\necho \"Init Script complete, executing arguments provided\"\nexec $@"

		if configmap.Data[client.DefaultScriptName] != expectedData {
			t.Error(fmt.Errorf("Unexpected contents of config map, got:\n%s \nexpect:\n%s", configmap.Data[client.DefaultScriptName], expectedData))
		}

		// Create the deployment we run the init script created from wherever
		// we've decided to mount it.
		err = dc.Client.Deploy(c.containerImage, c.tempMountPath,
			c.configMountPath+client.DefaultScriptName,
			c.cmdArgs, configmap.ObjectMeta.Name,
			c.configMountPath, c.requiredPorts)
		if err != nil {
			t.Error(err.Error())
		}

		// Now the deployment will be waiting for an attach to copy the binary
		// to boot from.

		dir, err = ioutil.TempDir("", "deploy")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir) // clean up

		resourcepath := dir + "/resources"

		// Create empty resource file:
		resources := &cloud.Resources{
			ResourceName: "Kubernetes",
			Details:      make(map[string]string),
			PrivateKey:   "",
			Servers:      make(map[string]*cloud.Server)}

		file, err := os.OpenFile(resourcepath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			t.Error(fmt.Errorf("failed to open resource file %s for writing: %s", resourcepath, err))
		}

		encoder := gob.NewEncoder(file)
		err = encoder.Encode(resources)
		if err != nil {
			t.Error(fmt.Errorf("Failed to encode resource file: %s", err))
		}

		// Generate certs
		caFile := dir + "/ca.pem"
		certFile := dir + "/cert.pem"
		keyFile := dir + "/key.pem"
		wrDir := "/wr-tmp/.wr_production/"
		err = internal.GenerateCerts(caFile, certFile, keyFile, "localhost")
		if err != nil {
			t.Errorf("failed to generate certificates: %s", err)
		}

		dc.Opts = &kubedeployment.DeployOpts{
			Files:         []client.FilePair{{"/tmp/wr", "/wr-tmp/"}, {caFile, wrDir}, {certFile, wrDir}, {keyFile, wrDir}},
			RequiredPorts: c.requiredPorts,
			ResourcePath:  resourcepath,
			Logger:        log15.New(),
		}

		// Start Controller
		stopCh := make(chan struct{})
		defer close(stopCh)
		go func() {
			dc.Run(stopCh)
		}()

		// Don't move this to a new test, the call to connect() waits and keeps
		// the controller running, this allows time for the manager to be bootstrapped.
		jq, err := jobqueue.Connect("localhost:8080", caFile, "localhost", []byte{}, 27*time.Second)
		if err != nil {
			t.Errorf("Failed to connect to jobqueue: %s", err)
		}
		if jq.ServerInfo.Mode != "started" {
			t.Errorf("Jobqueue not started, current mode : %s", jq.ServerInfo.Mode)
		} else {
			t.Logf("jobqueue server mode: %s", jq.ServerInfo.Mode)
		}

		t.Logf("Deployment Controller test passed")
	}
}
