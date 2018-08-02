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
	"testing"

	"k8s.io/client-go/kubernetes"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var tc client.Kubernetesp
var clientset kubernetes.Interface
var autherr error

func init() {
	tc = client.Kubernetesp{}
	clientset, _, autherr = tc.Authenticate()
	if autherr != nil {
		panic(autherr)
	}
	_ = tc.CreateNewNamespace("wr-testing")
	// Use the default namesace to avoid mess when testing on
	// non ephemeral clusters
	autherr = tc.Initialize(clientset, "wr-testing")
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

