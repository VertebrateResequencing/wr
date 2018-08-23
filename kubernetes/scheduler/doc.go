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

package scheduler

/*

Package scheduler is a kubernetes controller to oversee the scheduling of
wr-runner pods to a kubernetes cluster. It is an adapter that communicates with
the scheduleri implementation in ../../jobqueue/scheduler/kubernetes.go, and
provides answers to requests that require up to date information about what is
going on inside a cluster.

This package uses client-go: https://github.com/kubernetes/client-go Reading on
controllers:

    1. https://engineering.bitnami.com/articles/a-deep-dive-into-kubernetes-controllers.html
    2. https://github.com/kubernetes/community/blob/master/contributors/devel/controllers.md
    3. https://engineering.bitnami.com/articles/kubewatch-an-example-of-kubernetes-custom-controller.html
    4. https://github.com/kubernetes/client-go/tree/master/examples/workqueue
    5. https://github.com/kubernetes/sample-controller/blob/master/docs/controller-client-go.md
    6. https://github.com/kubernetes/sample-controller

    API Discussion, interesting but not crucial:
        * https://github.com/kubernetes/community/blob/master/contributors/design-proposals/api-machinery/controller-ref.md



The scheduling controller is initialised in
../../jobqueue/scheduler/kubernetes.go (initialize()), where the ScheduleOpts is
populated (from the manager cmd). This sets the default files to copy to all
runners, and defines the call back channels for communication between the
controller and the scheduleri implementation

The controller monitors pods and nodes in the cluster.

Pods are watched for init containers to copy tarballs to in the same way the
deployment controller does. They also report status changes of a pod to the
scheduleri implementation. This logic is in processPod(). Reporting is done over
a specific channel defined at pod creation (see runCmd in
../../jobqueue/scheduler/kubernetes.go)

When runCmd makes a call to client.Spawn() it also creates a PodAlive request
that is handled by the podAliveHandler(), which will store a copy of the pod,
it's error channel and a done bool. Once encountered by processPod() a pod's
channel is retrieved by pod name. Then when processPod observes a successfully
completed pod it will notify on the error channel by sending nil and then
setting the done bool to true. Any failures will result in a failure being sent
down that channel instead. A failure will also result in a message being sent as
a callback to the end user (via the web ui, sendErrChan()) containing the last
25 lines of logs from that pod (configurable). The done bool is important as the
pod lifecycle means that the same pod will be added to the work queue multiple
times throughout its deletion. We only want to send on the channel once, and the
done bool indicates if we have.

A pod alive request is:

    type PodAlive struct {
    Pod     *corev1.Pod // The pod that the ErrChan is for
    ErrChan chan error // ErrChan to send errors on
    Done    bool
    }

sent down a channel defined when the controller is created.

A node's available resources are also monitored, in order to fulfil the
reqCheck() in ../../jobqueue/scheduler/kubernetes.go, a cached total of all
resources available to each node is kept in memory for the controller. A request
is submitted in a similar manner to the way PodAlive requests are, and the
relevant channel is returned a nil error or the reason for the failure. This
informs the wr scheduler to decide if it's worth submitting a request for a
runner.

Currently, checks are only against RAM and Number of cores if running on a
cluster with node-level ephemeral storage tracking disabled. Kubernetes 1.10
enables this by default. The line 'n.StorageEphemeral().Cmp(req.Disk) != -1 '
checks this. With this turned off, this always passes so it is possible to
schedule a job that requires more disk than the node has. This can lead to
interesting failures. The concept of Ephemeral storage on a node goes against
the kubernetes model of using persistent volumes, and so is currently a beta
feature where the API may change. Currently, the user is warned about this.

A request / response will look like:

    // Request contains relevant information
    // for processing a request from reqCheck().
    type Request struct {
        RAM    resource.Quantity
        Time   time.Duration
        Cores  resource.Quantity
        Disk   resource.Quantity
        Other  map[string]string
        CbChan chan Response
    }

    // Response contains relevant information for
    // responding to a Request from reqCheck()
    type Response struct {
        Error     error
        Ephemeral bool // indicate if ephemeral storage is enabled
    }

Each response contains an ephemeral bool that informs the scheduler if it should
request ephemeral storage.

*/
