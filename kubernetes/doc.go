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

package kubedoc

/*
This is intended as a high-level overview of all kubernetes related code in the
wr codebase, and an explanation of how it fits together. A general understanding
of kubernetes concepts is assumed, along with an understanding of client-go.
(https://github.com/kubernetes/client-go)

See the links in the deployment and scheduler controller docs for information on
controllers. (Also commonly referred to operators)

Below is a diagram showing the packages involved in each step of the deployment
and operation of the wr kubernetes scheduler. It is also the recommended reading
order for each package's documentation.

                   +--------------+
                   |client package|
                   +------+-------+
                          ^
                          |
                   +------+------+
                   |kubeDeployCmd|
                   +------+------+
                          |
                          ^
               +----------+----------+
               |deployment controller|
               +----------+----------+
                          |
                          ^
                     +----+-----+
                     |managerCmd|
                     +---+--+---+
                         |  |
+--------------------------------------------------------------+
|scheduleri              |  |                                  |
| +----------+           |  |          +---------------------+ |
| |k8s driver| <---------+  +--------> |scheduling controller| |
| +--+--+----+                         +----------+--+-------+ |
|    ^  |                                         ^  |         |
|    |  +-----------------------------------------+  |         |
|    +-----------------------------------------------+         |
+--------------------------------------------------------------+

The client package is used by all of the packages to authenticate, and for
interacting with kuberenetes with basic tasks (create a pod or service, execute
a command in a container or port forwarding).

When a user first starts wr in kubernetes mode the kubeDeployCmd
(../cmd/kubernetes.go) will attempt to connect to a manager on the configured
port, if one exists it will terminate. Then it will configure and deploy wr to a
kubernetes cluster. Importantly it reads in a post creation script if specified.
It then checks if it can authenticate against the cluster before daemonising.
The child creates a resource file and rewrites any config files passed to be
able to tar them across to the init container. It then specifies the requested
configuration in a DeployOpts struct, that is passed to the deployment
controller, which is then run. It also generates the options that are passed to
the manager command inside the cluster.

The deployment controller calls Deploy() in the client package to create the
deployment in the cluster and then bootstraps that deployment by copying the
requested files. Once complete it starts port forwarding.

The manager command contains the options for the k8s driver
(../jobqueue/scheduler/kubernetes.go) and scheduling controller. These are
passed through to the driver, whereupon calling initialize() the scheduling
controller is started. The scheduling controller and k8s driver together work to
implement scheduleri, the interface wr requires to be satisfied in order to
implement a new scheduler.

The overall design is similar to:
https://github.com/GoogleCloudPlatform/spark-on-k8s-operator/blob/master/docs/design.md

*/
