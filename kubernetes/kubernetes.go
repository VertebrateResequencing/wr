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

package kubernetes

/*
Package kubernetes provides functions to interact with a kubernetes cluster, used to
create resources so that you can spawn runners, then delete those
resources when you're done.
*/

import (

	//"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/inconshreveable/log15"
	"github.com/sevlyar/go-daemon"
	appsv1beta1 "k8s.io/api/apps/v1beta1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	typedappsv1beta1 "k8s.io/client-go/kubernetes/typed/apps/v1beta1"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

type kubernetesi interface{}

type Quota struct{}

type kubernetesp struct {
	clientset         kubernetes.Interface
	clusterConfig     *rest.Config
	RESTClient        rest.Interface
	namespaceClient   typedv1.NamespaceInterface
	deploymentsClient typedappsv1beta1.DeploymentInterface
	serviceClient     typedv1.ServiceInterface
	podClient         typedv1.PodInterface
	PortForwarder     portForwarder
	StopChannel       chan struct{}
	ReadyChannel      chan struct{}
	cmdOut, cmdErr    io.Writer
	kubeconfig        *string
	newNamespaceName  string
	context           *daemon.Context
	log15.Logger
}

func int32Ptr(i int32) *int32 { return &i }

func boolPtr(b bool) *bool { return &b }

func (p *kubernetesp) createNewNamespace(name string) error {
	_, nsErr := p.namespaceClient.Create(&apiv1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	})
	if nsErr != nil {
		return nsErr
	}
	return nil
}

// Authenticate with cluster, return clientset.
// Optionally supply a logger
func (p *kubernetesp) Authenticate(logger ...log15.Logger) (kubernetes.Interface, *rest.Config, error) {
	var l log15.Logger
	if len(logger) == 1 {
		l = logger[0].New()
	} else {
		l = log15.New()
		l.SetHandler(log15.DiscardHandler())
	}
	p.Logger = l

	//Determine if in cluster.
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")

	switch {
	case len(host) == 0 || len(port) == 0:
		clusterConfig, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		// creates the clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			panic(err.Error())
		}
		return clientset, clusterConfig, nil

	default:
		var kubeconfig *string
		//Obtain cluster authentication information from users home directory, or fall back to user input.
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()

		var err error
		clusterConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			p.Logger.Error("Build cluster configuration from ~/.kube", "err", err)
			panic(err)
		}
		//Create authenticated clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			p.Logger.Error("Create authenticated clientset", "err", err)
			panic(err)
		}
		return clientset, clusterConfig, nil
	}
}

// initialise uses the passed clientset to
// authenticated create some clients used in other methods.
// also creates a new namespace for wr to work in
func (p *kubernetesp) Initialize(clientset kubernetes.Interface) error {
	// Create REST client
	p.RESTClient = clientset.CoreV1().RESTClient()
	// Create namespace client
	p.namespaceClient = clientset.CoreV1().Namespaces()
	// Create a unique namespace
	generatedName := strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
	fmt.Printf("GeneratedName: %v \n", generatedName)
	p.newNamespaceName = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
	fmt.Printf("newNamespaceName: %v \n", p.newNamespaceName)
	// Retry if namespace taken
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		nsErr := p.createNewNamespace(p.newNamespaceName)
		if nsErr != nil {
			fmt.Printf("Failed to create new namespace, %s. Trying again. Error: %v", p.newNamespaceName, nsErr)
			p.Logger.Warn("Failed to create new namespace. Trying again.", "namespace", p.newNamespaceName, "err", nsErr)
			p.newNamespaceName = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
		}
		return nsErr
	})
	if retryErr != nil {
		p.Logger.Error("Creation of new namespace failed", "err", retryErr)
		return fmt.Errorf("Creation of new namespace failed: %v", retryErr)
	}

	// Create client for deployments that is authenticated against the given cluster. Use default namsespace.
	p.deploymentsClient = clientset.AppsV1beta1().Deployments(p.newNamespaceName)

	// Create client for services
	p.serviceClient = clientset.CoreV1().Services(p.newNamespaceName)

	// Create client for pods
	p.podClient = clientset.CoreV1().Pods(p.newNamespaceName)

	// Create portforwarder
	//p.PortForwarder =

	// ToDO: This assumes one portforward per cluter deployment
	// This should probably go in pod and the channels be created in an options struct
	// to better isolate the logic
	// Make channels for port forwarding
	p.StopChannel = make(chan struct{}, 1)
	p.ReadyChannel = make(chan struct{})
	fmt.Println("Created channels for portforward")

	return nil
}

// Deploys wr manager to the namespace created by initialize()
// Copies binary with name wr_linux in the current working directory.
// Uses containerImage as the base docker image to build on top of
// Assumes tar is available.
func (p *kubernetesp) Deploy(containerImage string, mountPath string, files []filePair, binaryPath string, binaryArgs []string, requiredPorts []int) error {
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
							Command: []string{binaryPath},
							Args:    binaryArgs,
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "wr-temp",
									MountPath: mountPath,
								},
							},
							Env: []apiv1.EnvVar{
								{
									Name:  "WR_MANAGERPORT",
									Value: strconv.Itoa(requiredPorts[0]),
								},
								{
									Name:  "WR_MANAGERWEB",
									Value: strconv.Itoa(requiredPorts[1]),
								},
							},
							SecurityContext: &apiv1.SecurityContext{
								Privileged: boolPtr(true),
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
									MountPath: mountPath,
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
	fmt.Printf("Created deployment %q in namespace %v.\n", result.GetObjectMeta().GetName(), p.newNamespaceName)
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
			ClusterIP: "None",
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
	fmt.Printf("Created service %q in namespace %v.\n", svcResult.GetObjectMeta().GetName(), p.newNamespaceName)

	//Copy WR to pod, selecting by label.
	//Wait for the pod to be created, then return it
	var podList *apiv1.PodList
	getPodErr := wait.ExponentialBackoff(retry.DefaultRetry, func() (done bool, err error) {
		var getErr error
		podList, getErr = p.clientset.CoreV1().Pods(p.newNamespaceName).List(metav1.ListOptions{
			LabelSelector: "app=wr-manager",
		})
		switch {
		case getErr != nil:
			panic(fmt.Errorf("failed to list pods in namespace %v", p.newNamespaceName))
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

	// //Get the current working directory.
	// dir, err := os.Getwd()
	// if err != nil {
	// 	p.Logger.Error("Failed to get current working directory", "err", err)
	// 	return err
	// }

	//Set up new pipe
	pipeReader, pipeWriter := io.Pipe()

	//avoid deadlock by using goroutine
	go func() {
		defer pipeWriter.Close()
		//[]filePair{{dir + "/.wr_config.yml", "/wr-tmp/"}, {dir + "/wr-linux", "/wr-tmp/"}}
		tarErr := makeTar(files, pipeWriter)
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

	stdOut := new(Writer)
	stdErr := new(Writer)
	// Pass no command []string, so just attach to running command in initcontainer.
	opts := &CmdOptions{
		StreamOptions: StreamOptions{
			PodName:       pod.ObjectMeta.Name,
			ContainerName: pod.Spec.InitContainers[0].Name,
			Stdin:         true,
			In:            pipeReader,
			Out:           stdOut,
			Err:           stdErr,
		},
	}

	p.AttachCmd(opts)

	fmt.Printf("Contents of stdOut: %v\n", stdOut.Str)
	fmt.Printf("Contents of stdErr: %v\n", stdErr.Str)

	fmt.Println("Sleeping for 15s") // wait for container to be running
	time.Sleep(15 * time.Second)

	// This return should be executed by the child. Daemonising code below.
	// This way the portforward function knows nothing about how it's been daemonised.

	p.context = &daemon.Context{
		PidFileName: "portForwardPid",
		PidFilePerm: 0644,
		WorkDir:     "/",
		Umask:       027,
		Args:        []string{},
	}

	child, err := p.context.Reborn()
	if err != nil {
		panic(fmt.Errorf("failed to daemonize: %s", err))
	}

	if child != nil {
		return nil
	}
	defer p.context.Release()

	fmt.Println("Port Forward Daemon Started")

	// Interupt handler: On interupt close p.StopChannel.
	signals := make(chan os.Signal, 1)   // channel to receive interrupt
	signal.Notify(signals, os.Interrupt) // Notify on interrupt
	defer signal.Stop(signals)           // stop relaying signals to signals

	// Avoid deadlock using goroutine
	// <-signals is blocking
	go func() {
		<-signals
		if p.StopChannel != nil { // If stop channel is open
			//Closing StopChannel terminates the forward request
			close(p.StopChannel)
		}
	}()
	go p.PortForward(pod.ObjectMeta.Name, requiredPorts)

	err = daemon.ServeSignals()
	if err != nil {
		panic(fmt.Errorf("Error calling daemon.ServeSignals(): %v\n", err))
	}

	fmt.Println("Port Forward Daemon Stopped")

	return nil
}

// No required env variables
func (p *kubernetesp) requiredEnv() []string {
	return []string{}
}

// No required env variables
func (p *kubernetesp) maybeEnv() []string {
	return []string{""}
}

// Check if we are in a WR pod.
// As we control the hostname, just check if
// the hostname contains 'wr' in addition to the
// standard environment variables.
func (p *kubernetesp) inWRPod() bool {
	hostname, err := os.Hostname()
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	inCloud := false
	if err == nil && len(host) != 0 && len(port) != 0 {
		if strings.Contains(hostname, "wr") {
			inCloud = true
		}
	}
	return inCloud
}

// Silly numbers, will eventually update.
// Basically 'assume it will work'
func (p *kubernetesp) getQuota() (*Quota, error) {
	return &Quota{}, nil

}

// Spawn a new pod that contains a runner. Return the name.
func (p *kubernetesp) Spawn(baseContainer string, mountPath string, files []filePair, binaryPath string, binaryArgs []string, resources *ResourceRequest) (*Pod, error) {
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
					Name:    "wr-runner",
					Image:   baseContainer,
					Command: []string{binaryPath},
					Args:    binaryArgs,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "wr-temp",
							MountPath: mountPath,
						},
					},
					SecurityContext: &apiv1.SecurityContext{
						Privileged: boolPtr(true),
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
							MountPath: mountPath,
						},
					},
				},
			},
			Hostname: "wr-runner",
		},
	}
	//Create pod
	pod, err := p.podClient.Create(pod)
	if err != nil {
		p.Logger.Error("Failed to create pod", "err", err)
	}
	// //Copy WR to newly created pod
	// //Get the current working directory
	// dir, err := os.Getwd()
	// if err != nil {
	// 	p.Logger.Error("Failed to get current working directory", "err", err)
	// 	panic(err)
	// }

	//Set up new pipe
	pipeReader, pipeWriter := io.Pipe()

	//avoid deadlock by using goroutine
	go func() {
		defer pipeWriter.Close()
		//[]filePair{{dir + "/.wr_config.yml", "/wr-tmp/"}, {dir + "/wr-linux", "/wr-tmp/"}}
		tarErr := makeTar(files, pipeWriter)
		if tarErr != nil {
			p.Logger.Error("Error writing tar", "err", tarErr)
			panic(tarErr)
		}
	}()

	// Copy the wr binary to the running pod
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

	stdOut := new(Writer)
	stdErr := new(Writer)
	// Pass no command []string, so just attach to running command in initcontainer.
	opts := &CmdOptions{
		StreamOptions: StreamOptions{
			PodName:       pod.ObjectMeta.Name,
			ContainerName: pod.Spec.InitContainers[0].Name,
			Stdin:         true,
			In:            pipeReader,
			Out:           stdOut,
			Err:           stdErr,
		},
	}

	p.AttachCmd(opts)

	fmt.Printf("Contents of stdOut: %v\n", stdOut.Str)
	fmt.Printf("Contents of stdErr: %v\n", stdErr.Str)
	return &Pod{
		ID:        string(pod.ObjectMeta.UID),
		Name:      pod.ObjectMeta.Name,
		Resources: resources,
	}, nil
}

// Deletes the namespace created for wr.
func (p *kubernetesp) TearDown() error {
	err := p.namespaceClient.Delete(p.newNamespaceName, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting namespace", "err", err, "namespace", p.newNamespaceName)
		return err
	}
	return nil
}

// Deletes the given pod, doesn't check it exists first.
func (p *kubernetesp) DestroyPod(podName string) error {
	err := p.podClient.Delete(podName, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting pod", "err", err, "pod", podName)
		return err
	}
	return nil
}

// Checks a given pod exists. If it does, return the status
func (p *kubernetesp) CheckPod(podName string) (working bool, err error) {
	pod, err := p.podClient.Get(podName, metav1.GetOptions{})
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

// Closes StopChannel, this stops the
// portforward.
func (p *kubernetesp) KillPortForward() {
	//might need to modify all code to close the channel on interrupt instead as I'm not too sure i can attach to it
	d, err := p.context.Search()
	if err != nil {
		panic(fmt.Errorf("Failed to send signal to daemon: %v\n", err))
	}
	daemon.SendCommands(d)
	return
}
