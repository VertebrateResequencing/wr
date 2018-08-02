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
	"testing"

	"k8s.io/client-go/kubernetes"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	kubedeployment "github.com/VertebrateResequencing/wr/kubernetes/deployment"
)

var dc kubedeployment.Controller
var clientset kubernetes.Interface
var autherr error

func init() {

	// Build wr binary

	dc := kubedeployment.Controller{
		Client: &client.Kubernetesp{},
	}
	clientset, _, autherr = dc.Client.Authenticate()
	if autherr != nil {
		panic(autherr)

	}
	// avoid mess when testing on non ephemeral clusters
	_ = dc.Client.CreateNewNamespace("wr-testing")

	autherr = dc.Client.Initialize(clientset, "wr-testing")
	if autherr != nil {
		panic(autherr)
	}
	dc.Opts = &kubedeployment.DeployOpts{
		Files:         []client.FilePair{},
		RequiredPorts: []int{80, 8080},
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
	}{
		{
			containerImage:  "ubuntu:latest",
			tempMountPath:   "/wr-tmp/",
			cmdArgs:         []string{"tail", "-f", "/dev/null"},
			configMountPath: "/scripts",
			configMapData:   "echo hello world",
			requiredPorts:   []int{80, 8080},
		},
	}
	for _, c := range cases {

		// Test the creation of config maps (2 birds one stone)
		// We won't delete this now so we can use it later.
		configmap, err := dc.Client.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
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

	}

}
