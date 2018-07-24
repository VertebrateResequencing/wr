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

package client

/*

Package client provides functions to interact with a kubernetes cluster, used to
create resources so that you can spawn runners, then delete those
resources when you're done. Everything is centred around a Kubernetesp struct.
Calling Authenticate() will allow AttachCmd and ExecCmd to work without needing
to Initialize()

This package uses client-go: https://github.com/kubernetes/client-go

Authenticate will try 3 ways to authenticate against a cluster, initially it will try
and read in the masterUrl and kubeconfigPath from the users home directory, if that fails then
it will attempt to check the env variable KUBECONFIG for a path to alternate configuration.
Finally it will fall back to in cluster config which uses the service account kubernetes gives
to pods, expecting to be run in a kubernetes environment. It also returns and stores a clientset,
clusterConfig and REST Client in p.clientset, p.clusterConfig, p.RESTClient. The reason it's done
like this is because it allows a lighter use of the client library (e.g tearing down, exec various
commands) without needing to call Initialize()

Initialize() takes the passed clientset and optionally a namespace and creates various clients
that other client library functions need. If no namespace is passed it creates a unique namespace
ending '-wr'. It also creates p.configMapHashes a map of the contents of a config map (post creaation
script) and it's respective MD5 hash. This way when NewConfigMap is called (e.g in 'wr add --cloud_script sc.sh',
potentially many times with the same script) the function does not create a new config map, it retrieves
the name of the previously created script and returns it. This should reduce overall load on the API server.

The bulk of the work is done by Deploy() and Spawn().

Deploy() creates a cluster role binding that grants the default service account in the created namespace
permissions to run the scheduling controller. Then creates a kubernetes deployment (and by extension replicaset)
for the pod that will run the wr-manager and a service to make it hostname addressable.

Spawn() uses nearly the same pod spec as Deploy() does but does not create a service or new cluster role binding
for each pod spawned.

The pod spec for both Deploy() and Spawn() has an init container that will be running the latest tagged
ubuntu image and the command '/bin/tar", "-xf", "-"', which will exit after stdin has been attached once.
This is how the wr binary and relevant config files are copied to the cluster. The deployment controller
handles all of this. The options for the deployment controller are set in /cmd/kubernetes.go, the
deployment controller docs explain how it works. A limitation of this is that both Deploy() and Spawn()
only define one empty volume, with one mount point to be retained across containers. Currently this dir is
being redefined $HOME, and everything rewritten to be relative to ~/, anything that can't be is being
silently ignored, and will not survive the copy. (Will be lost in the filesystem of the init container.)
It is possible to define more than one (n) volume mounts, for n different paths, but this introduces more
complexity and I've not done it. It's a possiblity, however it breaks from the kubernetes concept of having
a persistent volume, and a persistent volume claim.

pod.go contains pod specific functions, notably AttachCmd, ExecCmd and PortForward. All three allow the
deployment of the manager and runners to occur. It also contains the code for creating the tarball
copied by the deployment controller, and getting the last n lines of logs from a wr-runner container
in a pod.

Possible Improvements:

As of kubernetes 1.10 (March 2018) config maps now allow binary data to be stored in them, as well as
strings. Exploiting this to copy the wr binaries and configuration files to the manager and runners
will be more simple, less restrictive and quicker. However, at the time of writing (July 2018), most
leading kubernetes providers are still running 1.9.X as default, and the Kubespray deployment method
(that we use on our internal openstack) only supports up to 1.9.5. The tar method of bootstrapping should
work on clusters going back to very early versions of kubernetes.

Mutltiple directories copied for the tar step? (See above)

A different way to deal with privileged = true needing to be set for FUSE to work.
See: https://github.com/kubernetes/kubernetes/issues/7890. Currently I can only see
this being an issue if a cluster is being shared by multiple users (managed service
such as OpenShift?)

A rough timeline / overview of the tasks I've undertaken to get to here can be found: https://trello.com/b/JoKK4lXo/kanban-work

*/
