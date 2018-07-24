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

/*

Package scheduler a kubernetes controller to oversee the scheduling of wr-runner
pods to a kubernetes cluster, It is an adapter that communicates wit the
scheduleri implementation in ../../jobqueue/scheduler/kubernetes.go, and
provides answers to questions that require up to date information about
what is going on inside a cluster.

This package uses client-go: https://github.com/kubernetes/client-g
Reading on controllers:

	1. https://engineering.bitnami.com/articles/a-deep-dive-into-kubernetes-controllers.html
	2. https://github.com/kubernetes/community/blob/master/contributors/devel/controllers.md
	3. https://engineering.bitnami.com/articles/kubewatch-an-example-of-kubernetes-custom-controller.html
	4. https://github.com/kubernetes/client-go/tree/master/examples/workqueue
	5. https://github.com/kubernetes/sample-controller/blob/master/docs/controller-client-go.md
	6. https://github.com/kubernetes/sample-controller

	API Discussion, interesting but not crucial:
		* https://github.com/kubernetes/community/blob/master/contributors/design-proposals/api-machinery/controller-ref.md



The scheduling controller is initialised in ../../jobqueue/scheduler/kubernetes.go (initialize()), where
the ScheduleOpts is populated (from the manager cmd). This sets the default files to copy to all runners,
and defines the call back channels for communication between the controller and the scheduleri implementation

The controller monitors pods and nodes in the cluster.

Pods are watched for init containers to copy tarballs to in the same way the deployment controller does and
to report status changes of a pod to the scheduleri implementation. This logic is in processPod(). Reporting
is done over specific channels defined at pod creation (runCmd in ../../jobqueue/scheduler/kubernetes.go)
where the channel is retrieved by pod name. Currently when runCmd makes a Spawn() request, it also creates
a PodAlive request that is handled by the podAliveHandler, which will store a copy of the pod, it's
respective error channel and a done bool. Then when processPod observes a succesfully completed pod it will
notify on that error channel, by sending nil and then setting the done bool to true. Any failures will result
in a failure being sent down that channel instead. A failure will also result in a message being sent as a
callback to the end user (via the web ui) containing the last 25 lines of logs from that pod (configurable).

A nodes avaliable resources are also monotired, in order to fulfil the reqCheck() in ../../jobqueue/scheduler/kubernetes.go,
a cached total of all resources avaliable to each node is kept in memory for the controller. A request is submitted
in a similar manner to the way PodAlive requests are, and the relevant channel is returned a nil error or the
reason for the failure. This informes the wr scheduler if it worth submitting a request for a runner.Controller
Currently checks are only against RAM and Number of cores, Kubernetes 1.10 allows nodes to report ephemeral storage
and the line 'n.StorageEphemeral().Cmp(req.Disk) != -1 ' checks this. On clusters below 1.10, this always passes so
it is possible to schedule a job that requires more disk than the node has.

*/
