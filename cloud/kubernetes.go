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

package cloud

// This file contains a provideri implementation for Kubernetes

import (
	"archive/tar"
	//"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/inconshreveable/log15"
	appsv1beta1 "k8s.io/api/apps/v1beta1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedappsv1beta1 "k8s.io/client-go/kubernetes/typed/apps/v1beta1"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

type kubernetesp struct {
	clusterConfig     *rest.Config
	clientset         *kubernetes.Clientset
	namespaceClient   typedv1.NamespaceInterface
	deploymentsClient typedappsv1beta1.DeploymentInterface
	serviceClient     typedv1.ServiceInterface
	podClient         typedv1.PodInterface
	kubeconfig        *string
	newNamespace      string
	log15.Logger
}

// Writer provides a method for writing output (from stderr)
type Writer struct {
	Str []string
}

// Write writes output (from stderr)
func (w *Writer) Write(p []byte) (n int, err error) {
	str := string(p)
	if len(str) > 0 {
		w.Str = append(w.Str, str)
	}
	return len(str), nil
}

func int32Ptr(i int32) *int32 { return &i }

// Adds file to tar.Writer
func addFile(tw *tar.Writer, fpath string, dest string) error {
	file, err := os.Open(fpath)
	if err != nil {
		return err
	}
	defer file.Close()
	if stat, err := file.Stat(); err == nil {
		// now lets create the header as needed for this file within the tarball
		header := new(tar.Header)
		header.Name = dest + path.Base(fpath)
		header.Size = stat.Size()
		header.Mode = int64(stat.Mode())
		header.ModTime = stat.ModTime()
		// write the header to the tarball archive
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		// copy the file data to the tarball
		if _, err := io.Copy(tw, file); err != nil {
			return err
		}
	}
	return nil
}

// Writes tarball to an io.writer
func makeTar(files []string, destDir string, writer io.Writer) error {
	//Set up tar writer
	tarWriter := tar.NewWriter(writer)
	defer tarWriter.Close()
	//Add each file to the tarball
	for i := range files {
		if err := addFile(tarWriter, path.Clean(files[i]), destDir); err != nil {
			panic(err)
		}
	}
	return nil
}

// initialise uses required configuration files to authenticate with
// the given Kubernetes cluster and to create some clients used in other methods.
// also creates a new namespace for wr to work in
func (p *kubernetesp) initialize(logger log15.Logger) error {
	p.Logger = logger.New("cloud", "kubernetes")
	//Obtain cluster authentication information from users home directory, or fall back to user input.
	if home := homedir.HomeDir(); home != "" {
		p.kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		p.kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	var err error
	p.clusterConfig, err = clientcmd.BuildConfigFromFlags("", *p.kubeconfig)
	if err != nil {
		p.Logger.Error("Build cluster configuration from ~/.kube", "err", err)
		return err
	}
	//Create authenticated clientset
	p.clientset, err = kubernetes.NewForConfig(p.clusterConfig)
	if err != nil {
		p.Logger.Error("Create authenticated clientset", "err", err)
		return err
	}
	//Create a unique namespace
	p.namespaceClient = p.clientset.CoreV1().Namespaces()
	p.newNamespace = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
	//Retry if namespace taken
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, nsErr := p.namespaceClient.Create(&apiv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: p.newNamespace,
			},
		})
		if nsErr != nil {
			fmt.Printf("Failed to create new namespace, %s. Trying again. Error: %v", p.newNamespace, err)
			p.Logger.Warn("Failed to create new namespace. Trying again.", "namespace", p.newNamespace, "err", err)
			p.newNamespace = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
		}
		return nsErr
	})
	if retryErr != nil {
		p.Logger.Error("Creation of new namespace failed", "err", retryErr)
		return fmt.Errorf("Creation of new namespace failed: %v", retryErr)
	}

	//Create client for deployments that is authenticated against the given cluster. Use default namsespace.
	p.deploymentsClient = p.clientset.AppsV1beta1().Deployments(p.newNamespace)

	//Create client for services
	p.serviceClient = p.clientset.CoreV1().Services(p.newNamespace)

	//Create client for pods
	p.podClient = p.clientset.CoreV1().Pods(p.newNamespace)

	return nil
}

// Deploys wr manager to the namespace created by initialize()
// Copies binary with name wr_linux in the current working directory.
// Uses ubuntu:17.10 as the docker image to build on top of.
// Assumes tar is available
// ToDo : port forward 1021, 1022 to local machine
// for now do something like
// kubectl port-forward  wr-manager-68d74d9dd6-qzsqk :1022 --namespace trusting-shirley-wr
func (p *kubernetesp) deploy(resources *Resources, requiredPorts []int, gatewayIP, cidr string, dnsNameservers []string) error {
	//Specify new wr deployment
	deployment := &appsv1beta1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wr-manager",
		},
		Spec: appsv1beta1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "wr-manager",
					Labels: map[string]string{
						"app": "wr-manager",
					},
				},
				Spec: apiv1.PodSpec{
					Volumes: []apiv1.Volume{
						{
							Name: "wr-temp",
							VolumeSource: apiv1.VolumeSource{
								EmptyDir: &apiv1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []apiv1.Container{
						{
							Name:  "wr-manager",
							Image: "ubuntu:17.10",
							Ports: []apiv1.ContainerPort{
								{
									Name:          "wr-manager",
									Protocol:      apiv1.ProtocolTCP,
									ContainerPort: int32(requiredPorts[0]),
								},
								{
									Name:          "wr-web",
									Protocol:      apiv1.ProtocolTCP,
									ContainerPort: int32(requiredPorts[1]),
								},
							},
							Command: []string{
								"/wr-tmp/wr",
							},
							Args: []string{
								"manager",
								"start",
								"-f",
							},
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "wr-temp",
									MountPath: "/wr-tmp",
								},
							},
						},
					},
					InitContainers: []apiv1.Container{
						{
							Name:      "init-container",
							Image:     "ubuntu:17.10",
							Command:   []string{"/bin/tar", "-xf", "-"},
							Stdin:     true,
							StdinOnce: true,
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "wr-temp",
									MountPath: "/wr-tmp",
								},
							},
						},
					},
					Hostname: "wr-manager",
				},
			},
		},
	}

	// Create Deployment
	fmt.Println("Creating deployment...")
	result, err := p.deploymentsClient.Create(deployment)
	if err != nil {
		p.Logger.Error("Creating deployment", "err", err)
		return err
	}
	fmt.Printf("Created deployment %q in namespace %v.\n", result.GetObjectMeta().GetName(), p.newNamespace)
	// specify service so wr-manager can be resolved using kubedns
	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wr-manager",
			Labels: map[string]string{
				"app": "wr-manager",
			},
		},
		Spec: apiv1.ServiceSpec{
			Selector: map[string]string{
				"app": "wr-manager",
			},
			ClusterIP: "none",
			Ports: []apiv1.ServicePort{
				{
					Name: "wr-manager",
					Port: int32(requiredPorts[0]),
					TargetPort: intstr.IntOrString{
						IntVal: int32(requiredPorts[0]),
					},
				},
			},
		},
	}

	svcResult, err := p.serviceClient.Create(service)
	if err != nil {
		p.Logger.Error("Creating service", "err", err)
		return err
	}
	fmt.Printf("Created service %q in namespace %v.\n", svcResult.GetObjectMeta().GetName(), p.newNamespace)

	//Copy WR to pod, selecting by label.
	//Wait for the pod to be created, then return it
	var podList *apiv1.PodList
	getPodErr := wait.ExponentialBackoff(retry.DefaultRetry, func() (done bool, err error) {
		var getErr error
		podList, getErr = p.clientset.CoreV1().Pods(p.newNamespace).List(metav1.ListOptions{
			LabelSelector: "app=wr-manager",
		})
		switch {
		case getErr != nil:
			panic(fmt.Errorf("failed to list pods in namespace %v", p.newNamespace))
		case len(podList.Items) == 0:
			return false, nil
		case len(podList.Items) > 0:
			return true, nil
		default:
			return false, err
		}
	})
	if getPodErr != nil {
		p.Logger.Error("Failed to list pods", "err", err)
		return fmt.Errorf("failed to list pods, error: %v", getPodErr)

	}

	//Get the current working directory.
	dir, err := os.Getwd()
	if err != nil {
		p.Logger.Error("Failed to get current working directory", "err", err)
		return err
	}

	//Set up new pipe
	reader, writer := io.Pipe()

	//avoid deadlock by using goroutine
	go func() {
		defer writer.Close()
		tarErr := makeTar([]string{dir + "/wr-linux"}, "/wr-tmp/", writer)
		if tarErr != nil {
			p.Logger.Error("Error writing tar", "err", tarErr)
			panic(tarErr)
		}
	}()

	//Copy the wr binary to the running pod
	fmt.Println("Sleeping for 15s") // wait for container to be running
	time.Sleep(15 * time.Second)
	fmt.Println("Woken up")
	pod := podList.Items[0]
	fmt.Printf("Container for pod is %v\n", pod.Spec.InitContainers[0].Name)
	fmt.Println(pod.Spec.InitContainers)
	fmt.Printf("Pod has name %v, in namespace %v\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)

	//Make a request to the APIServer for an 'attach'.
	//Open Stdin and Stderr for use by the client
	execRequest := p.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.ObjectMeta.Name).
		Namespace(pod.ObjectMeta.Namespace).
		SubResource("attach")
	execRequest.VersionedParams(&apiv1.PodExecOptions{
		Container: pod.Spec.InitContainers[0].Name,
		Stdin:     true,
		Stdout:    false,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	//Create an executor to send commands / receive output.
	//SPDY Allows multiplexed bidirectional streams to and from  the pod
	exec, err := remotecommand.NewSPDYExecutor(p.clusterConfig, "POST", execRequest.URL())
	if err != nil {
		p.Logger.Error("Error creating SPDYExecutor", "err", err)
		return fmt.Errorf("Error creating SPDYExecutor: %v", err)
	}
	fmt.Println("Created SPDYExecutor")

	stdIn := reader
	stdErr := new(Writer)

	//Execute the command, with Std(in,out,err) pointing to the
	//above readers and writers
	fmt.Println("Executing remotecommand")
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  stdIn,
		Stderr: stdErr,
		Tty:    false,
	})
	if err != nil {
		p.Logger.Error("Error executing remote command", "err", err, "StdErr", stdErr)
		fmt.Printf("StdErr: %v\n", stdErr)
		return fmt.Errorf("Error executing remote command: %v", err)
	}

	return nil
}

// No required env variables
func (p *kubernetesp) requiredEnv() []string {
	return []string{""}
}

// No required env variables
func (p *kubernetesp) maybeEnv() []string {
	return []string{""}
}

// Check if we are in a pod.
// As we control the hostname, just check if
// the hostname contains 'wr'
func (p *kubernetesp) inCloud() bool {
	hostname, err := os.Hostname()
	inCloud := false
	if err == nil {
		if strings.Contains(hostname, "wr") {
			inCloud = true
		}
	}
	return inCloud
}

// Silly numbers, will eventually update.
// Basically 'assume it will work'
func (p *kubernetesp) getQuota() (*Quota, error) {
	return &Quota{
		MaxRAM:        999999999,
		MaxCores:      999999999,
		MaxInstances:  999999999,
		MaxVolume:     999999999,
		UsedRAM:       0,
		UsedCores:     0,
		UsedInstances: 0,
		UsedVolume:    0,
	}, nil

}

func (p *kubernetesp) flavors() map[string]*Flavor {
	flavors := make(map[string]*Flavor)
	flavors["1"] = &Flavor{
		ID:    "1",
		Name:  "Kubernetes",
		Cores: 32,
		RAM:   524288,
		Disk:  12,
	}
	return flavors

}

// Pods don't really map to servers. Lots of awful hackery incoming.
// I'm sorry in advance
// Spawn a new pod that contains a runner. Return the name.
func (p *kubernetesp) spawn(resources *Resources, osPrefix string, flavorID string, diskGB int, externalIP bool, usingQuotaCh chan bool) (serverID, serverIP, serverName, adminPass string, err error) {
	//Generate a pod name?
	//podName := "wr-runner-" + strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1)
	//Create a new pod.
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-runner",
			Labels: map[string]string{
				"wr": "runner",
			},
		},
		Spec: apiv1.PodSpec{
			Volumes: []apiv1.Volume{
				{
					Name: "wr-temp",
					VolumeSource: apiv1.VolumeSource{
						EmptyDir: &apiv1.EmptyDirVolumeSource{},
					},
				},
			},
			Containers: []apiv1.Container{
				{
					Name:  "wr-runner",
					Image: "ubuntu:17.10",
					Command: []string{
						"/wr-tmp/wr-linux",
					},
					Args: []string{
						"runner",
						"start",
						"--server",
						"wr-manager." + p.newNamespace + ".svc.cluster.local:1021",
					},
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "wr-temp",
							MountPath: "/wr-tmp",
						},
					},
				},
			},
			InitContainers: []apiv1.Container{
				{
					Name:      "init-container",
					Image:     "ubuntu:17.10",
					Command:   []string{"/bin/tar", "-xf", "-"},
					Stdin:     true,
					StdinOnce: true,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "wr-temp",
							MountPath: "/wr-tmp",
						},
					},
				},
			},
		},
	}
	//Create pod
	pod, err = p.podClient.Create(pod)
	if err != nil {
		p.Logger.Error("Failed to create pod", "err", err)
	}
	//Copy WR to newly created pod
	//Get the current working directory.
	dir, err := os.Getwd()
	if err != nil {
		p.Logger.Error("Failed to get current working directory", "err", err)
		panic(err)
	}

	//Set up new pipe
	reader, writer := io.Pipe()

	//avoid deadlock on the writer by using goroutine
	go func() {
		defer writer.Close()
		tarErr := makeTar([]string{dir + "/wr-linux"}, "/wr-tmp/", writer)
		if tarErr != nil {
			p.Logger.Error("Error writing tar", "err", tarErr)
			panic(tarErr)
		}
	}()

	//Copy the wr binary to the running pod
	fmt.Println("Sleeping for 15s") // wait for container to be running TODO: Use watch instead
	time.Sleep(15 * time.Second)
	fmt.Println("Woken up")
	fmt.Println("Getting updated pod") //TODO: Use watch channel to notify on success
	pod, err = p.podClient.Get(pod.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		p.Logger.Error("Error getting updated pod", "err", err)
		panic(err)
	}
	fmt.Printf("Container for pod is %v\n", pod.Spec.InitContainers[0].Name)
	fmt.Println(pod.Spec.InitContainers)
	fmt.Printf("Pod has name %v, in namespace %v\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)

	//Make a request to the APIServer for an 'attach'.
	//Open Stdin and Stderr for use by the client
	execRequest := p.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.ObjectMeta.Name).
		Namespace(pod.ObjectMeta.Namespace).
		SubResource("attach")
	execRequest.VersionedParams(&apiv1.PodExecOptions{
		Container: pod.Spec.InitContainers[0].Name,
		Stdin:     true,
		Stdout:    false,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	//Create an executor to send commands / receive output.
	//SPDY Allows multiplexed bidirectional streams to and from  the pod
	exec, err := remotecommand.NewSPDYExecutor(p.clusterConfig, "POST", execRequest.URL())
	if err != nil {
		p.Logger.Error("Error creating SPDYExecutor", "err", err)
		panic(fmt.Errorf("Error creating SPDYExecutor: %v", err))
	}
	fmt.Println("Created SPDYExecutor")

	stdIn := reader
	stdErr := new(Writer)

	//Execute the command, with Std(in,out,err) pointing to the
	//above readers and writers
	fmt.Println("Executing remotecommand")
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  stdIn,
		Stderr: stdErr,
		Tty:    false,
	})
	if err != nil {
		p.Logger.Error("Error executing remote command", "err", err, "StdErr", stdErr)
		fmt.Printf("StdErr: %v\n", stdErr)
		panic(fmt.Errorf("Error executing remote command: %v", err))
	}
	serverID = string(pod.ObjectMeta.UID)
	serverIP = "null" // maybe set to pod ip / node ip later if required
	serverName = pod.ObjectMeta.Name
	adminPass = "null"
	return
}

//Deletes the namespace created for wr.
func (p *kubernetesp) tearDown(resources *Resources) error {
	err := p.namespaceClient.Delete(p.newNamespace, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting namespace", "err", err, "namespace", p.newNamespace)
		return err
	}
	return nil
}

//Deletes the given pod, doesn't check it exists first.
func (p *kubernetesp) destroyServer(serverID string) error {
	err := p.podClient.Delete(serverID, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting pod", "err", err, "pod", serverID)
		return err
	}
	return nil
}

//Checks a given pod exists. If it does, return the status
func (p *kubernetesp) checkServer(serverID string) (working bool, err error) {
	pod, err := p.podClient.Get(serverID, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	switch {
	case pod.Status.Phase == "Running":
		return true, nil
	case pod.Status.Phase == "Pending":
		return true, nil
	case pod.Status.Phase == "Succeeded":
		return false, nil
	case pod.Status.Phase == "Failed":
		return false, nil
	case pod.Status.Phase == "Unknown":
		return false, nil
	default:
		return false, nil
	}
}
