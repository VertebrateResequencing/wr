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

package deployment_test

import (
	"encoding/gob"
	"fmt"
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
)

var dc kubedeployment.Controller
var autherr error
var testingNamespace string

// Just test that the call to Deploy() works, and that when configured as
// expected, the deployment controller will copy the tarball, and that the
// manager can be connected to.

func init() {

	dc = kubedeployment.Controller{
		Client: &client.Kubernetesp{},
	}
	dc.Clientset, dc.Restconfig, autherr = dc.Client.Authenticate()
	if autherr != nil {
		panic(autherr)
	}

	rand.Seed(time.Now().UnixNano())
	testingNamespace = strings.Replace(namesgenerator.GetRandomName(1), "_", "-", -1) + "-wr-testing"

	_ = dc.Client.CreateNewNamespace(testingNamespace)

	autherr = dc.Client.Initialize(dc.Clientset, testingNamespace)
	if autherr != nil {
		panic(autherr)
	}

}

func TestDeploy(t *testing.T) {
	cases := []struct {
		containerImage  string
		tempMountPath   string
		cmdArgs         []string
		configMountPath string
		configMapData   string
		requiredPorts   []int
		resourcePath    string
	}{
		{
			containerImage:  "ubuntu:latest",
			tempMountPath:   "/wr-tmp/",
			cmdArgs:         []string{"/wr-tmp/wr", "manager", "start", "-f"},
			configMountPath: "/scripts/",
			configMapData:   "echo \"hello world\"",
			requiredPorts:   []int{8080, 8081},
			resourcePath:    "/tmp/resources",
		},
	}
	for _, c := range cases {

		// Test the creation of config maps (2 birds one stone)
		// We won't delete this now so we can use it later.
		configmap, err := dc.Client.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
		}

		expectedData := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"" +
			"\necho \"hello world\"\necho \"Init Script complete, executing arguments provided\"\nexec $@"

		if configmap.Data[client.DefaultScriptName] != expectedData {
			t.Error(fmt.Errorf("Unexpected contents of config map, got:\n%s \nexpect:\n%s", configmap.Data[client.DefaultScriptName], expectedData))
		}

		// Create the deployment
		// we run the init script created from wherever we've
		// decided to mount it.
		err = dc.Client.Deploy(c.containerImage, c.tempMountPath,
			c.configMountPath+client.DefaultScriptName,
			c.cmdArgs, configmap.ObjectMeta.Name,
			c.configMountPath, c.requiredPorts)
		if err != nil {
			t.Error(err.Error())
		}

		// Now the deployment will be waiting for
		// an attach to copy the binary to boot from.

		// Create empty resource file:
		resources := &cloud.Resources{
			ResourceName: "Kubernetes",
			Details:      make(map[string]string),
			PrivateKey:   "",
			Servers:      make(map[string]*cloud.Server)}

		file, err := os.OpenFile(c.resourcePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			t.Error(fmt.Errorf("failed to open resource file %s for writing: %s", c.resourcePath, err))
		}

		encoder := gob.NewEncoder(file)
		err = encoder.Encode(resources)
		if err != nil {
			t.Error(fmt.Errorf("Failed to encode resource file: %s", err))
		}

		// Generate certs
		caFile := "/tmp/ca.pem"
		certFile := "/tmp/cert.pem"
		keyFile := "/tmp/key.pem"
		wrDir := "/wr-tmp/.wr_production/"
		err = internal.GenerateCerts(caFile, certFile, keyFile, "localhost")
		if err != nil {
			t.Errorf("failed to generate certificates: %s", err)
		}

		dc.Opts = &kubedeployment.DeployOpts{
			Files:         []client.FilePair{{"/tmp/wr", "/wr-tmp/"}, {caFile, wrDir}, {certFile, wrDir}, {keyFile, wrDir}},
			RequiredPorts: c.requiredPorts,
			ResourcePath:  c.resourcePath,
			Logger:        log15.New(),
		}

		// Start Controller
		stopCh := make(chan struct{})
		defer close(stopCh)
		go func() {
			dc.Run(stopCh)
		}()

		// Test if the manager is up.
		jq, err := jobqueue.Connect("localhost:8080", caFile, "localhost", []byte{}, 27*time.Second)
		if err != nil {
			t.Errorf("Failed to connect to jobqueue: %s", err)
		}

		t.Logf("jobqueue server mode: %s", jq.ServerInfo.Mode)
		t.Logf("Deployment Controller test passed")

	}

}
