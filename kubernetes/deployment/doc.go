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

package deployment

/*

Package deployment is a kubernetes controller that oversees the deployment of
the wr scheduler controller and manager into a kubernetes cluster. It copies
configuration files, binaries and handles port forwarding.

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

The Deployment controller calls client.Deploy() in order to create the
deployment for the wr manager and then bootstraps it. If an existing deployment
is found (another pod running, with the label "app=wr-manager") it skips this
Deploy() and just runs the controller, which will update the resource file and
start port forwarding.

This controller watches for a pod in the provided namespace with the label
"app=wr-manager", when it sees a pod with an init container in the running state
it calls CopyTar() (in ../client/pod.go) with the files provided to it when it
starts. (That are set in kubeDeployCmd (../../cmd/kubernetes.go). CopyTar
(io.)Pipes a tarball with the requested files to the container and disconnects
once complete. This triggers tar to exit and the init container completes. The
main (wr-manager) container then starts, and runs the wr binary that is copied
in the previous step. A current limitation to this step is that we are only
attaching one folder (see client doc.go).

Once the wr-manager container is seen to be running (success!) the name of the
pod containing it is stored in the resources file for later use, (it is used to
retrieve logs and client.token from) then PortForward is called on the required
ports set in the deployment options, this will be a port for the web ui and
another to communicate for the manager.

*/
