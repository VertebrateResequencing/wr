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
*/

import (
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
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

	// Allow auth against gcp cluster
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
)

// DefaultScriptName is the name
// to use as the default script in
// the create configmap functions.
const DefaultScriptName = "wr-boot"

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
	configMapHashes   map[string][16]byte
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
	_, nsErr := p.clientset.CoreV1().Namespaces().Create(&apiv1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	})
	if nsErr != nil {
		return nsErr
	}
	return nil
}

// Authenticate with cluster, return clientset and RESTConfig.
// Can be called from within or outside of a cluster, should still work.
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
	host, port, kubevar := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"), os.Getenv("KUBECONFIG")

	switch {
	case (len(host) == 0 || len(port) == 0) && len(kubevar) == 0:
		p.Logger.Info("authenticating using information from user's home directory")
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
			p.Logger.Error("failed to build cluster configuration from ~/.kube", "err", err)
			panic(err)
		}
		//Create authenticated clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			p.Logger.Error("creating authenticated clientset", "err", err)
			panic(err)
		}
		// Set up internal clientset and clusterConfig
		p.Logger.Info("succesfully authenticated using information from user's home directory")
		p.clientset = clientset
		p.clusterConfig = clusterConfig
		// Create REST client
		p.RESTClient = clientset.CoreV1().RESTClient()
		return clientset, clusterConfig, nil
	case len(kubevar) != 0:
		p.Logger.Info(fmt.Sprintf("authenticating using $KUBECONFIG: %s", kubevar))
		var kubeconfig *string
		kubeconfig = &kubevar

		var err error
		clusterConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			p.Logger.Error(fmt.Sprintf("failed to build cluster configuration from %s", kubevar), "err", err)
			panic(err)
		}
		//Create authenticated clientset
		clientset, err := kubernetes.NewForConfig(clusterConfig)
		if err != nil {
			p.Logger.Error("creating authenticated clientset", "err", err)
			panic(err)
		}
		// Set up internal clientset and clusterConfig
		p.Logger.Info("succesfully authenticated")
		p.clientset = clientset
		p.clusterConfig = clusterConfig
		// Create REST client
		p.RESTClient = clientset.CoreV1().RESTClient()
		return clientset, clusterConfig, nil

	default:
		p.Logger.Info("authenticating using InClusterConfig()")
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
		// Create REST client
		p.RESTClient = clientset.CoreV1().RESTClient()
		p.Logger.Info("succesfully authenticated using InClusterConfig()")
		return clientset, clusterConfig, nil

	}

}

// Initialize uses the passed clientset to
// create some authenticated clients used in other methods.
// Creates a new namespace for wr to work in.
// Optionally pass a namespace as a string.
func (p *Kubernetesp) Initialize(clientset kubernetes.Interface, namespace ...string) error {

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
				p.Logger.Warn("failed to create new namespace. Trying again.", "namespace", p.NewNamespaceName, "err", nsErr)
				p.NewNamespaceName = strings.Replace(namesgenerator.GetRandomName(0), "_", "-", -1) + "-wr"
			}
			return nsErr
		})
		if retryErr != nil {
			p.Logger.Error("creation of new namespace failed", "err", retryErr)
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

	// Store md5 hashes of the data in each configMap.
	// used when calling newConfigMap().
	p.configMapHashes = map[string][16]byte{}

	// ToDO: This assumes one portforward per namespaced deployment
	// This should probably go in pod and the channels be created in an options struct
	// to better isolate the logic

	// Make channels for port forwarding
	p.StopChannel = make(chan struct{}, 1)
	p.ReadyChannel = make(chan struct{})

	return nil
}

// Deploy creates the wr-manager deployment and service.
// Creates ClusterRoleBinding to allow the default service account
// in the namespace rights to manage cluster.
// (ToDo: Write own ClusterRole that allows fewer permissions)
// Copying of WR to initcontainer done by Controller when ready
// (Assumes tar is available).
// Portforwarding done by controller when ready.
// ContainerImage is the Image used for the manager pod
// tempMountPath is the path at which the 'wr-tmp' directory is set to. It is also set to $HOME
// command is the command to be executed in the container
// cmdArgs are the arguments to pass to the supplied command
// configMapName is the name of the configmap to mount at the configMountPath provided. Usually this contains the
func (p *Kubernetesp) Deploy(containerImage string, tempMountPath string, command string, cmdArgs []string, configMapName string, configMountPath string, requiredPorts []int) error {
	// Patch the default cluster role for to allow
	// pods and nodes to be viewed.
	_, err := p.clientset.RbacV1().ClusterRoleBindings().Create(&rbacapi.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-cluster-role-binding-",
			Labels:       map[string]string{"wr-" + p.NewNamespaceName: "ClusterRoleBinding"},
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
							Image: "ubuntu:latest",
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
							Command: []string{command},
							Args:    cmdArgs,
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
							Lifecycle: &apiv1.Lifecycle{
								PreStop: &apiv1.Handler{
									Exec: &apiv1.ExecAction{
										Command: []string{"cat", tempMountPath + ".wr_development/log",
											"cat", tempMountPath + ".wr_development/kubeSchedulerLog",
											"cat", tempMountPath + ".wr_development/kubeSchedulerControllerLog"},
									},
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
		p.Logger.Error("creating deployment", "err", err)
		return err
	}
	fmt.Printf("Created deployment %q in namespace %v.\n", result.GetObjectMeta().GetName(), p.NewNamespaceName)
	// specify service so wr-manager can be resolved using kubedns if needed
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
		p.Logger.Error("creating service", "err", err)
		return err
	}

	/*
	   No longer copying tarball to pod here,
	   instead the deployment controller waits on the
	   status of the InitContainer to be running, then
	   runs CopyTar, found in pod.go

	*/

	return nil
}

// InWRPod() checks if we are in a WR pod.
// As we control the hostname, just check if
// the hostname contains 'wr' in addition to the
// standard environment variables.
func InWRPod() bool {
	hostname, err := os.Hostname()
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	inPod := false
	if err == nil && len(host) != 0 && len(port) != 0 {
		if strings.Contains(hostname, "wr") {
			inPod = true
		}
	}
	return inPod
}

// Spawn a new pod that contains a runner. Return the name.
func (p *Kubernetesp) Spawn(baseContainerImage string, tempMountPath string, binaryPath string, binaryArgs []string, configMapName string, configMountPath string, resources apiv1.ResourceRequirements) (*apiv1.Pod, error) {
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-runner-",
			Labels: map[string]string{
				"wr": "runner",
			},
		},
		Spec: apiv1.PodSpec{
			RestartPolicy: apiv1.RestartPolicyNever,
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
						{
							Name:  "USER",
							Value: "root",
						},
					},
					Resources: resources,
					SecurityContext: &apiv1.SecurityContext{
						Privileged: boolPtr(true),
						Capabilities: &apiv1.Capabilities{
							Add: []apiv1.Capability{"SYS_ADMIN"},
						},
					},
				},
			},
			InitContainers: []apiv1.Container{
				{
					Name:      "runner-init-container",
					Image:     "ubuntu:latest",
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
	// Create pod
	pod, err := p.podClient.Create(pod)
	if err != nil {
		p.Logger.Error("failed to create pod", "err", err)
		return nil, err
	}

	/*
	   No longer copying tarball to pod here,
	   instead the deployment controller waits on the
	   status of the InitContainer to be running, then
	   runs CopyTar, found in pod.go

	*/

	return pod, err
}

// TearDown deletes the namespace and cluster role binding created for wr.
func (p *Kubernetesp) TearDown(namespace string) error {
	err := p.clientset.CoreV1().Namespaces().Delete(namespace, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("deleting namespace", "err", err, "namespace", namespace)
		return err
	}

	crbl, err := p.clientset.RbacV1().ClusterRoleBindings().List(metav1.ListOptions{
		LabelSelector: "wr-" + namespace,
	})
	if err != nil {
		p.Logger.Error("getting ClusterRoleBindings", "err", err)
		return err
	}
	err = p.clientset.RbacV1().ClusterRoleBindings().Delete(crbl.Items[0].ObjectMeta.Name, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("deleting ClusterRoleBinding", "ClusterRoleBinding", crbl.Items[0].ObjectMeta.Name, "err", err)
		return err
	}

	return nil
}

// DestroyPod deletes the given pod, doesn't check it exists first.
func (p *Kubernetesp) DestroyPod(podName string) error {
	err := p.podClient.Delete(podName, &metav1.DeleteOptions{})
	if err != nil {
		p.Logger.Error("deleting pod", "err", err, "pod", podName)
		return err
	}
	return nil
}

// CheckPod checks a given pod exists. If it does, return the status.
// Currently not used anywhere
// func (p *Kubernetesp) CheckPod(podName string) (working bool, err error) {
// 	pod, err := p.podClient.Get(podName, metav1.GetOptions{})
// 	if err != nil {
// 		return false, err
// 	}
// 	switch {
// 	case pod.Status.Phase == "Running":
// 		return true, nil
// 	case pod.Status.Phase == "Pending":
// 		return true, nil
// 	case pod.Status.Phase == "Succeeded":
// 		return false, nil
// 	case pod.Status.Phase == "Failed":
// 		return false, nil
// 	case pod.Status.Phase == "Unknown":
// 		return false, nil
// 	default:
// 		return false, nil
// 	}
// }

// NewConfigMap creates a new configMap
// Kubernetes 1.10 (Released Late March 2018)
// provides a BinaryData field that could be used to
// replace the initContainer method for copying the executable.
// At the moment is not appropriate as it's not likely most users are
// running 1.10
func (p *Kubernetesp) NewConfigMap(opts *ConfigMapOpts) (*apiv1.ConfigMap, error) {
	//Check if we have already created a config map with a script with the same hash.

	// Calculate hash of opts.Data, json stringify it first.
	jsonData, err := json.Marshal(opts.Data)
	if err != nil {
		return nil, err
	}
	md5 := md5.Sum([]byte(jsonData)) //#nosec

	var match string
	for k, v := range p.configMapHashes {
		if v == md5 {
			match = k
		}

	}
	if len(match) != 0 {
		configMap, err := p.configMapClient.Get(match, metav1.GetOptions{})
		return configMap, err
	}

	// No configmap with the data we want exists, so create one.
	configMap, err := p.configMapClient.Create(&apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "wr-script-",
		},
		Data: opts.Data,
	})

	// Store the name and md5 in configMapHashes
	p.configMapHashes[configMap.ObjectMeta.Name] = md5

	return configMap, err
}

// CreateInitScriptConfigMapFromFile is the same as CreateInitScriptConfigMap
// but takes a path to a file as input.
func (p *Kubernetesp) CreateInitScriptConfigMapFromFile(scriptPath string) (*apiv1.ConfigMap, error) {
	// read in the script given
	buf, err := ioutil.ReadFile(scriptPath)
	if err != nil {
		return nil, err
	}
	// stringify
	script := string(buf)

	// Insert script into template
	top := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"\n"
	bottom := "\necho \"Init Script complete, executing arguments provided\"\nexec $@"

	cmap, err := p.NewConfigMap(&ConfigMapOpts{
		Data: map[string]string{DefaultScriptName: top + script + bottom},
	})

	return cmap, err

}

// CreateInitScriptConfigMap performs very basic string fudging.
// This allows a wr pod to execute some arbitrary script before starting the runner / manager.
// So far it appears to work
func (p *Kubernetesp) CreateInitScriptConfigMap(script string) (*apiv1.ConfigMap, error) {

	// Insert script into template
	top := "#!/usr/bin/env bash\nset -euo pipefail\necho \"Running init script\"\n"
	bottom := "\necho \"Init Script complete, executing arguments provided\"\nexec $@"

	cmap, err := p.NewConfigMap(&ConfigMapOpts{
		Data: map[string]string{DefaultScriptName: top + script + bottom},
	})

	return cmap, err

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
