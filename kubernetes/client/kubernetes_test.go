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

package client_test

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"strings"
	"testing"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	"github.com/docker/docker/pkg/namesgenerator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var tc client.Kubernetesp
var clientset kubernetes.Interface
var autherr error
var testingNamespace string

func init() {
	tc = client.Kubernetesp{}
	clientset, _, autherr = tc.Authenticate()
	if autherr != nil {
		panic(autherr)
	}

	rand.Seed(time.Now().UnixNano())
	testingNamespace = strings.Replace(namesgenerator.GetRandomName(1), "_", "-", -1) + "-wr-testing"

	_ = tc.CreateNewNamespace(testingNamespace)

	autherr = tc.Initialize(clientset, testingNamespace)
	if autherr != nil {
		panic(autherr)
	}
}

func TestCreateNewNamespace(t *testing.T) {
	cases := []struct {
		namespaceName string
	}{
		{
			namespaceName: "test",
		},
	}
	for _, c := range cases {
		err := tc.CreateNewNamespace(c.namespaceName)
		if err != nil {
			t.Error(err.Error())
		}
		_, err = clientset.CoreV1().Namespaces().Get(c.namespaceName, metav1.GetOptions{})
		if err != nil {
			t.Error(err.Error())
		}
		// Clean up
		err = clientset.CoreV1().Namespaces().Delete(c.namespaceName, &metav1.DeleteOptions{})
		if err != nil {
			t.Log("failed to clean up namespace", err.Error())
		}
	}
}

// Test that the Deploy() call returns without error.
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
			configMountPath: "/scripts/",
			configMapData:   "echo \"hello world\"",
			requiredPorts:   []int{80, 8080},
		},
	}
	for _, c := range cases {

		// Test the creation of config maps (2 birds one stone)
		// We won't delete this now so we can use it later.
		configmap, err := tc.CreateInitScriptConfigMap(c.configMapData)
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
		err = tc.Deploy(c.containerImage, c.tempMountPath,
			c.configMountPath+client.DefaultScriptName,
			c.cmdArgs, configmap.ObjectMeta.Name,
			c.configMountPath, c.requiredPorts)
		if err != nil {
			t.Error(err.Error())
		}

	}

}

func TestSpawn(t *testing.T) {
	cases := []struct {
		containerImage  string
		tempMountPath   string
		cmdArgs         []string
		configMountPath string
		configMapData   string
		resourceReq     apiv1.ResourceRequirements
	}{
		{
			containerImage:  "ubuntu:latest",
			tempMountPath:   "/wr-tmp/",
			cmdArgs:         []string{"tail", "-f", "/dev/null"},
			configMountPath: "/scripts/",
			configMapData:   "echo \"hello world\"",
			resourceReq: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					apiv1.ResourceCPU:              *resource.NewMilliQuantity(int64(1)*1000, resource.DecimalSI),
					apiv1.ResourceMemory:           *resource.NewQuantity(int64(1)*1024*1024, resource.BinarySI),
					apiv1.ResourceEphemeralStorage: *resource.NewQuantity(int64(0)*1024*1024*1024, resource.BinarySI),
				},
				Limits: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewMilliQuantity(int64(1)*1000, resource.DecimalSI),
					apiv1.ResourceMemory: *resource.NewQuantity(int64(1)*1024*1024, resource.BinarySI),
				},
			},
		},
	}
	for _, c := range cases {

		// Test the creation of config maps (2 birds one stone)
		// We won't delete this now so we can use it later.
		configmap, err := tc.CreateInitScriptConfigMap(c.configMapData)
		if err != nil {
			t.Error(err.Error())
		}

		expectedData := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"" +
			"\necho \"hello world\"\necho \"Init Script complete, executing arguments provided\"\nexec $@"

		if configmap.Data[client.DefaultScriptName] != expectedData {
			t.Error(fmt.Errorf("Unexpected contents of config map, got:\n%s \nexpect:\n%s", configmap.Data[client.DefaultScriptName], expectedData))
		}

		// create spawn request
		// we run the init script created from wherever we've
		// decided to mount it.

		pod, err := tc.Spawn(c.containerImage, c.tempMountPath,
			c.configMountPath+client.DefaultScriptName,
			c.cmdArgs, configmap.ObjectMeta.Name,
			c.configMountPath, c.resourceReq)
		if err != nil {
			t.Error(err.Error())
		}

		// wait on the status to be running or pending
		err = wait.Poll(500*time.Millisecond, wait.ForeverTestTimeout, func() (bool, error) {
			pod, err = clientset.CoreV1().Pods(testingNamespace).Get(pod.ObjectMeta.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if pod.Status.Phase == "Pending" || pod.Status.Phase == "Running" {
				return true, nil
			}
			return false, nil
		})

		if err != nil {
			t.Error(fmt.Errorf("Spawn failed: %s", err))
		}

		// Now delete it
		err = tc.DestroyPod(pod.ObjectMeta.Name)
		if err != nil {
			t.Error(fmt.Errorf("Deleting pod failed: %s", err))
		}

	}

}

func TestInWrPod(t *testing.T) {
	// These tests are not run inside a wr pod. Expect false
	inwr := client.InWRPod()
	if inwr {
		t.Error("This test is not run inside a pod WR controlls, should return false.")
	}
}

func TestCreateInitScriptConfigMapFromFile(t *testing.T) {
	cases := []struct {
		fileData string
		filePath string
	}{
		{
			fileData: "echo \"hello world\"",
			filePath: "/tmp/d1",
		},
	}

	for _, c := range cases {
		err := ioutil.WriteFile(c.filePath, []byte(c.fileData), 0644)
		if err != nil {
			t.Error(fmt.Errorf("Failed to write file to %s: %s", c.filePath, err))
		}
		cmap, err := tc.CreateInitScriptConfigMapFromFile(c.filePath)

		expectedData := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"" +
			"\necho \"hello world\"\necho \"Init Script complete, executing arguments provided\"\nexec $@"

		if cmap.Data[client.DefaultScriptName] != expectedData {
			t.Error(fmt.Errorf("Unexpected contents of config map, got:\n%s \nexpect:\n%s", cmap.Data[client.DefaultScriptName], expectedData))
		}

	}

}

// Must be called after deploy()
func TestTearDown(t *testing.T) {
	cases := []struct {
		namespaceName string
	}{
		{
			namespaceName: testingNamespace,
		},
	}
	for _, c := range cases {
		err := tc.TearDown(c.namespaceName)
		if err != nil {
			t.Error(err.Error())
		}
		err = tc.TearDown(c.namespaceName)
		if err == nil {
			t.Error("TearDown should fail if called twice with the same input.")
		}

	}
}
