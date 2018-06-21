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
resources when you're done.
*/

import (

	//"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/inconshreveable/log15"
	"github.com/sevlyar/go-daemon"
	appsv1beta1 "k8s.io/api/apps/v1beta1"
	apiv1 "k8s.io/api/core/v1"
	rbacapi "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	typedappsv1beta1 "k8s.io/client-go/kubernetes/typed/apps/v1beta1"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"math/rand"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

type kubernetesi interface{}

// Kubernetesp is the implementation for the kubernetes
// cluster provider. It provides access to all methods
// defined in this package
type Kubernetesp struct {
	clientset         kubernetes.Interface
	clusterConfig     *rest.Config
	RESTClient        rest.Interface
	namespaceClient   typedv1.NamespaceInterface
	deploymentsClient typedappsv1beta1.DeploymentInterface
	serviceClient     typedv1.ServiceInterface
	podClient         typedv1.PodInterface
	configMapClient   typedv1.ConfigMapInterface
	PortForwarder     portForwarder
	StopChannel       chan struct{}
	ReadyChannel      chan struct{}
	cmdOut, cmdErr    io.Writer
	kubeconfig        *string
	NewNamespaceName  string
	context           *daemon.Context
	Logger            log15.Logger
}

// ConfigMapOpts defines the name and Data
// (Binary, or strings) to store in a ConfigMap
type ConfigMapOpts struct {
	// BinaryData for potential later use
	BinaryData []byte
	Data       map[string]string
	Name       string
}

// ServiceOpts defines basic options
// for a kubernetes service
type ServiceOpts struct {
	Name      string
	Labels    map[string]string
	Selector  map[string]string
	ClusterIP string
	Ports     []apiv1.ServicePort
}

func int32Ptr(i int32) *int32 { return &i }

func boolPtr(b bool) *bool { return &b }

// CreateNewNamespace Creates a new namespace with the provided name
func (p *Kubernetesp) CreateNewNamespace(name string) error {
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
func (p *Kubernetesp) Authenticate(logger ...log15.Logger) (kubernetes.Interface, *rest.Config, error) {
	var l log15.Logger
	if len(logger) == 1 {
		l = logger[0].New("clientlib", "true")
	} else {
		l = log15.New("clientlib")
		l.SetHandler(log15.DiscardHandler())
	}
	p.Logger = l

	//Determine if in cluster.
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")

	switch {
	case len(host) == 0 || len(port) == 0:
		p.Logger.Info("Authenticating using information from user's home directory")
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
			p.Logger.Error("Failed to build cluster configuration from ~/.kube", "err", err)
			panic(err)
		}
		//Create authenticated clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			p.Logger.Error("Creating authenticated clientset", "err", err)
			panic(err)
		}
		// Set up internal clientset and clusterConfig
		p.Logger.Info("Succesfully authenticated using information from user's home directory")
		p.clientset = clientset
		p.clusterConfig = clusterConfig
		return clientset, clusterConfig, nil

	default:
		p.Logger.Info("Authenticating using InClusterConfig()")
		clusterConfig, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		// creates the clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			panic(err.Error())
		}
		// Set up internal clientset and clusterConfig
		p.clientset = clientset
		p.clusterConfig = clusterConfig
		p.Logger.Info("Succesfully authenticated using InClusterConfig()")
		return clientset, clusterConfig, nil

	}

}

// Initialize uses the passed clientset to
// create some authenticated clients used in other methods.
// also creates a new namespace for wr to work in.
// Optionally pass a namespace as a string.
func (p *Kubernetesp) Initialize(clientset kubernetes.Interface, namespace ...string) error {
	// Create REST client
	p.RESTClient = clientset.CoreV1().RESTClient()
	// Create namespace client
	p.namespaceClient = clientset.CoreV1().Namespaces()
	// Create a unique namespace if one isn't passed
	if len(namespace) == 1 {
		p.NewNamespaceName = namespace[0]
	} else {
		rand.Seed(time.Now().UnixNano())
		p.NewNamespaceName = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
		fmt.Printf("NewNamespaceName: %v \n", p.NewNamespaceName)
		// Retry if namespace taken
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			nsErr := p.CreateNewNamespace(p.NewNamespaceName)
			if nsErr != nil {
				fmt.Printf("Failed to create new namespace, %s. Trying again. Error: %v", p.NewNamespaceName, nsErr)
				p.Logger.Warn("Failed to create new namespace. Trying again.", "namespace", p.NewNamespaceName, "err", nsErr)
				p.NewNamespaceName = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
			}
			return nsErr
		})
		if retryErr != nil {
			p.Logger.Error("Creation of new namespace failed", "err", retryErr)
			return fmt.Errorf("Creation of new namespace failed: %v", retryErr)
		}
	}

	// Create client for deployments that is authenticated against the given cluster. Use default namsespace.
	p.deploymentsClient = clientset.AppsV1beta1().Deployments(p.NewNamespaceName)

	// Create client for services
	p.serviceClient = clientset.CoreV1().Services(p.NewNamespaceName)

	// Create client for pods
	p.podClient = clientset.CoreV1().Pods(p.NewNamespaceName)

	// Create configMap client
	p.configMapClient = clientset.CoreV1().ConfigMaps(p.NewNamespaceName)

	// ToDO: This assumes one portforward per cluter deployment
	// This should probably go in pod and the channels be created in an options struct
	// to better isolate the logic

	// Make channels for port forwarding
	p.StopChannel = make(chan struct{}, 1)
	p.ReadyChannel = make(chan struct{})

	return nil
}

// Deploy creates the wr-manager deployment and service.
// Copying of WR to initcontainer now done by Controller when ready
// portforwarding now done by controller when ready
// *old* : Deploys wr manager to the namespace created by initialize()
// Copies binary with name wr_linux in the current working directory.
// Uses containerImage as the base docker image to build on top of
// Assumes tar is available.
func (p *Kubernetesp) Deploy(containerImage string, tempMountPath string, binaryPath string, binaryArgs []string, configMapName string, configMountPath string, requiredPorts []int) error {
	// Patch the default cluster role for to allow
	// pods and nodes to be viewed.
	_, err := p.clientset.RbacV1().ClusterRoleBindings().Create(&rbacapi.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-cluster-role-binding-",
		},
		Subjects: []rbacapi.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "default",
				Namespace: p.NewNamespaceName,
			},
		},
		RoleRef: rbacapi.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	})
	if err != nil {
		return fmt.Errorf("Failed to create cluster role binding in namespace %s: %s", p.NewNamespaceName, err)
	}
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
						{
							Name: configMapName,
							VolumeSource: apiv1.VolumeSource{
								ConfigMap: &apiv1.ConfigMapVolumeSource{
									LocalObjectReference: apiv1.LocalObjectReference{
										Name: configMapName,
									},
									DefaultMode: int32Ptr(0777),
								},
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
									MountPath: tempMountPath,
								},
								{
									Name:      configMapName,
									MountPath: configMountPath,
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
								{
									Name:  "HOME",
									Value: tempMountPath,
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
									MountPath: tempMountPath,
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
	fmt.Printf("Created deployment %q in namespace %v.\n", result.GetObjectMeta().GetName(), p.NewNamespaceName)
	// specify service so wr-manager can be resolved using kubedns
	svcOpts := ServiceOpts{
		Name: "wr-manager",
		Labels: map[string]string{
			"app": "wr-manager",
		},
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
	}
	err = p.CreateService(&svcOpts)
	if err != nil {
		p.Logger.Error("Creating service", "err", err)
		return err
	}

	/*
	   No longer copying tarball to pod here,
	   instead the deployment controller waits on the
	   status of the InitContainer to be running, then
	   runs CopyTar, found in pod.go

	*/

	// //Copy WR to pod, selecting by label.
	// //Wait for the pod to be created, then return it
	// var podList *apiv1.PodList
	// getPodErr := wait.ExponentialBackoff(retry.DefaultRetry, func() (done bool, err error) {
	// 	var getErr error
	// 	podList, getErr = p.clientset.CoreV1().Pods(p.NewNamespaceName).List(metav1.ListOptions{
	// 		LabelSelector: "app=wr-manager",
	// 	})
	// 	switch {
	// 	case getErr != nil:
	// 		panic(fmt.Errorf("failed to list pods in namespace %v", p.NewNamespaceName))
	// 	case len(podList.Items) == 0:
	// 		return false, nil
	// 	case len(podList.Items) > 0:
	// 		return true, nil
	// 	default:
	// 		return false, err
	// 	}
	// })
	// if getPodErr != nil {
	// 	p.Logger.Error("Failed to list pods", "err", err)
	// 	return fmt.Errorf("failed to list pods, error: %v", getPodErr)

	// }

	// //Get the current working directory.
	// dir, err := os.Getwd()
	// if err != nil {
	// 	p.Logger.Error("Failed to get current working directory", "err", err)
	// 	return err
	// }

	// //Set up new pipe
	// pipeReader, pipeWriter := io.Pipe()

	// //avoid deadlock by using goroutine
	// go func() {
	// 	defer pipeWriter.Close()
	// 	//[]filePair{{dir + "/.wr_config.yml", "/wr-tmp/"}, {dir + "/wr-linux", "/wr-tmp/"}}
	// 	tarErr := makeTar(files, pipeWriter)
	// 	if tarErr != nil {
	// 		p.Logger.Error("Error writing tar", "err", tarErr)
	// 		panic(tarErr)
	// 	}
	// }()

	// //Copy the wr binary to the running pod
	// fmt.Println("Sleeping for 15s") // wait for container to be running
	// time.Sleep(15 * time.Second)
	// fmt.Println("Woken up")
	// pod := podList.Items[0]
	// fmt.Printf("Container for pod is %v\n", pod.Spec.InitContainers[0].Name)
	// fmt.Println(pod.Spec.InitContainers)
	// fmt.Printf("Pod has name %v, in namespace %v\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)

	// stdOut := new(Writer)
	// stdErr := new(Writer)
	// // Pass no command []string, so just attach to running command in initcontainer.
	// opts := &CmdOptions{
	// 	StreamOptions: StreamOptions{
	// 		PodName:       pod.ObjectMeta.Name,
	// 		ContainerName: pod.Spec.InitContainers[0].Name,
	// 		Stdin:         true,
	// 		In:            pipeReader,
	// 		Out:           stdOut,
	// 		Err:           stdErr,
	// 	},
	// }

	// p.AttachCmd(opts)

	// fmt.Printf("Contents of stdOut: %v\n", stdOut.Str)
	// fmt.Printf("Contents of stdErr: %v\n", stdErr.Str)
	return nil
}

// No required env variables
func (p *Kubernetesp) requiredEnv() []string {
	return []string{}
}

// No required env variables
func (p *Kubernetesp) maybeEnv() []string {
	return []string{""}
}

// Check if we are in a WR pod.
// As we control the hostname, just check if
// the hostname contains 'wr' in addition to the
// standard environment variables.
func (p *Kubernetesp) inWRPod() bool {
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

// Spawn a new pod that contains a runner. Return the name.
func (p *Kubernetesp) Spawn(baseContainerImage string, tempMountPath string, binaryPath string, binaryArgs []string, configMapName string, configMountPath string, resources *ResourceRequest) (*apiv1.Pod, error) {
	//Generate a pod name?
	//podName := "wr-runner-" + strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1)
	//Create a new pod.
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-runner-",
			Labels: map[string]string{
				"wr": "runner",
			},
		},
		Spec: apiv1.PodSpec{
			RestartPolicy: apiv1.RestartPolicyOnFailure,
			Volumes: []apiv1.Volume{

				{
					Name: "wr-temp",
					VolumeSource: apiv1.VolumeSource{
						EmptyDir: &apiv1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: configMapName,
					VolumeSource: apiv1.VolumeSource{
						ConfigMap: &apiv1.ConfigMapVolumeSource{
							LocalObjectReference: apiv1.LocalObjectReference{
								Name: configMapName,
							},
							DefaultMode: int32Ptr(0777),
						},
					},
				},
			},
			Containers: []apiv1.Container{
				{
					Name:    "wr-runner",
					Image:   baseContainerImage,
					Command: []string{binaryPath},
					Args:    binaryArgs,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "wr-temp",
							MountPath: tempMountPath,
						},
						{
							Name:      configMapName,
							MountPath: configMountPath,
						},
					},
					Env: []apiv1.EnvVar{
						{
							Name:  "HOME",
							Value: tempMountPath,
						},
					},
					SecurityContext: &apiv1.SecurityContext{
						Privileged: boolPtr(true),
					},
				},
			},
			InitContainers: []apiv1.Container{
				{
					Name:      "runner-init-container",
					Image:     "ubuntu:17.10",
					Command:   []string{"/bin/tar", "-xf", "-"},
					Stdin:     true,
					StdinOnce: true,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "wr-temp",
							MountPath: tempMountPath,
						},
					},
				},
			},
		},
	}
	//Create pod
	pod, err := p.podClient.Create(pod)
	if err != nil {
		p.Logger.Error("Failed to create pod", "err", err)
		return nil, err
	}
	/*
	   No longer copying tarball to pod here,
	   instead the scheduling controller waits on the
	   status of the InitContainer to be running, then
	   runs CopyTar, found in pod.go

	*/

	// // //Copy WR to newly created pod
	// // //Get the current working directory
	// // dir, err := os.Getwd()
	// // if err != nil {
	// // 	p.Logger.Error("Failed to get current working directory", "err", err)
	// // 	panic(err)
	// // }

	// //Set up new pipe
	// pipeReader, pipeWriter := io.Pipe()

	// //avoid deadlock by using goroutine
	// go func() {
	// 	defer pipeWriter.Close()
	// 	//[]filePair{{dir + "/.wr_config.yml", "/wr-tmp/"}, {dir + "/wr-linux", "/wr-tmp/"}}
	// 	tarErr := makeTar(files, pipeWriter)
	// 	if tarErr != nil {
	// 		p.Logger.Error("Error writing tar", "err", tarErr)
	// 		panic(tarErr)
	// 	}
	// }()

	// // Copy the wr binary to the running pod
	// fmt.Println("Sleeping for 15s") // wait for container to be running TODO: Use watch instead
	// time.Sleep(15 * time.Second)
	// fmt.Println("Woken up")
	// fmt.Println("Getting updated pod") //TODO: Use watch channel to notify on success
	// pod, err = p.podClient.Get(pod.ObjectMeta.Name, metav1.GetOptions{})
	// if err != nil {
	// 	p.Logger.Error("Error getting updated pod", "err", err)
	// 	panic(err)
	// }
	// fmt.Printf("Container for pod is %v\n", pod.Spec.InitContainers[0].Name)
	// fmt.Println(pod.Spec.InitContainers)
	// fmt.Printf("Pod has name %v, in namespace %v\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)

	// stdOut := new(Writer)
	// stdErr := new(Writer)
	// // Pass no command []string, so just attach to running command in initcontainer.
	// opts := &CmdOptions{
	// 	StreamOptions: StreamOptions{
	// 		PodName:       pod.ObjectMeta.Name,
	// 		ContainerName: pod.Spec.InitContainers[0].Name,
	// 		Stdin:         true,
	// 		In:            pipeReader,
	// 		Out:           stdOut,
	// 		Err:           stdErr,
	// 	},
	// }

	// p.AttachCmd(opts)

	// fmt.Printf("Contents of stdOut: %v\n", stdOut.Str)
	// fmt.Printf("Contents of stdErr: %v\n", stdErr.Str)
	return pod, err
}

// TearDown deletes the namespace created for wr.
func (p *Kubernetesp) TearDown() error {
	err := p.namespaceClient.Delete(p.NewNamespaceName, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting namespace", "err", err, "namespace", p.NewNamespaceName)
		return err
	}
	return nil
}

// DestroyPod deletes the given pod, doesn't check it exists first.
func (p *Kubernetesp) DestroyPod(podName string) error {
	err := p.podClient.Delete(podName, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("Deleting pod", "err", err, "pod", podName)
		return err
	}
	return nil
}

// CheckPod checks a given pod exists. If it does, return the status
func (p *Kubernetesp) CheckPod(podName string) (working bool, err error) {
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

// NewConfigMap creates a new configMap
// Kubernetes 1.10 (Released Late March 2018)
// provides a BinaryData field that could be used to
// replace the initContainer method for copying the executable.
// At the moment is not appropriate as it's not likely most users are
// running 1.10
func (p *Kubernetesp) NewConfigMap(opts *ConfigMapOpts) (*apiv1.ConfigMap, error) {
	configMap, err := p.configMapClient.Create(&apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: opts.Name,
		},
		Data: opts.Data,
	})

	return configMap, err
}

// CreateInitScriptConfigMapFromFile is the same as CreateInitScriptConfigMap
// but takes a path to a file as input.
func (p *Kubernetesp) CreateInitScriptConfigMapFromFile(name string, scriptPath string) error {
	// read in the script given
	buf, err := ioutil.ReadFile(scriptPath)
	if err != nil {
		return err
	}
	// stringify
	script := string(buf)

	// Insert script into template
	top := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"\n"
	bottom := "\necho \"Init Script complete, executing arguments provided\"\nexec $@"

	_, err = p.NewConfigMap(&ConfigMapOpts{
		Name: name,
		Data: map[string]string{name + ".sh": top + script + bottom},
	})

	return err

}

// CreateInitScriptConfigMap performs very basic string fudging.
// This allows a wr pod to execute some arbitrary script before starting the runner / manager.
// So far it appears to work
func (p *Kubernetesp) CreateInitScriptConfigMap(name string, script string) error {

	// Insert script into template
	top := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"\n"
	bottom := "\necho \"Init Script complete, executing arguments provided\"\nexec $@"

	_, err := p.NewConfigMap(&ConfigMapOpts{
		Name: name,
		Data: map[string]string{name + ".sh": top + script + bottom},
	})

	return err

}

// CreateService Creates a service with the defined options from
// ServiceOpts
func (p *Kubernetesp) CreateService(opts *ServiceOpts) error {
	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   opts.Name,
			Labels: opts.Labels,
		},
		Spec: apiv1.ServiceSpec{
			Selector:  opts.Selector,
			ClusterIP: opts.ClusterIP,
			Ports:     opts.Ports,
		},
	}

	_, err := p.serviceClient.Create(service)
	return err
}
