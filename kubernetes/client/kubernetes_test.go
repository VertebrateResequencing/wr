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
	testclient "k8s.io/client-go/kubernetes/fake"
)

var tc client.Kubernetesp
var clientset kubernetes.Interface

func init() {
	tc = client.Kubernetesp{}
	clientset = testclient.NewSimpleClientset()
	tc.Initialize(clientset)
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
		// Do the thing
		err := tc.CreateNewNamespace(c.namespaceName)
		if err != nil {
			t.Fatal(err.Error())
		}
		_, err = clientset.CoreV1().Namespaces().Get(c.namespaceName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err.Error())
		}
	}
}
