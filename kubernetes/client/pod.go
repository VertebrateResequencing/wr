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

// This file contains the code for the Pod struct.

import (
	"strings"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"archive/tar"
	"net/url"
	//"errors"

	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"

	"github.com/inconshreveable/log15"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

// Pod contains some basic identifying information
// about a pod
type Pod struct {
	ID        string
	Name      string
	Resources *ResourceRequest
	logger    log15.Logger
}

// ResourceRequest specifies a
// request for resources. Used in Spawn()
type ResourceRequest struct {
	Cores int
	Disk  int
	RAM   int
}

// CmdOptions contains StreamOptions for use in AttachCmd()
// or ExecCmd(). The first item in the command slice must be
// the command to execute only, any following will be arguments.
type CmdOptions struct {
	StreamOptions

	Command []string
}

// StreamOptions specifies all resources
// needed to attach / run a command in a pod,
// and stream in StdIn / return StdOut & StdErr.
type StreamOptions struct {
	PodName       string
	ContainerName string
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
}

// FilePair is a source, destination
// pair of file paths
type FilePair struct {
	Src, Dest string
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
// Takes a slice of FilePair(s), format source, destination
func makeTar(files []FilePair, writer io.Writer) error {
	//Set up tar writer
	tarWriter := tar.NewWriter(writer)
	defer tarWriter.Close()
	// Add each file to the tarball
	fmt.Println(len(files))
	for i := range files {
		fmt.Printf("Adding file %v \n", files[i])
		if err := addFile(tarWriter, path.Clean(files[i].Src), files[i].Dest); err != nil {
			return err
		}
	}
	fmt.Println("Done adding files to tar")
	return nil
}

// AttachCmd attaches to a running container, pipes StdIn to the command running on that container
// if StdIn is supplied.
// Should work after only calling Authenticate()
func (p *Kubernetesp) AttachCmd(opts *CmdOptions) (stdOut, stdErr string, err error) {
	// Make a request to the APIServer for an 'attach'.
	// Open Stdin and Stderr for use by the client
	execRequest := p.RESTClient.Post().
		Resource("pods").
		Name(opts.PodName).
		Namespace(p.NewNamespaceName).
		SubResource("attach")
	execRequest.VersionedParams(&apiv1.PodExecOptions{
		Container: opts.ContainerName,
		Stdin:     opts.In != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	// Create an executor to send commands / receive output.
	// SPDY Allows multiplexed bidirectional streams to and from  the pod
	exec, err := remotecommand.NewSPDYExecutor(p.clusterConfig, "POST", execRequest.URL())
	if err != nil {
		panic(fmt.Errorf("Error creating SPDYExecutor: %v", err))
	}
	// Execute the command, with Std(in,out,err) pointing to the
	// above readers and writers
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  opts.In,
		Stdout: opts.Out,
		Stderr: opts.Err,
		Tty:    false,
	})
	if err != nil {
		fmt.Printf("StdErr: %v\n", opts.Err)
		panic(fmt.Errorf("Error executing remote command: %v", err))
	}
	return "", "", nil
}

// ExecCmd executes the provided command inside a running container, if StdIn is supplied
// pipes StdIn to the command.
// Should work after only calling Authenticate()
func (p *Kubernetesp) ExecCmd(opts *CmdOptions, namespace string) (stdOut, stdErr string, err error) {
	// Make Request to APISever to 'exec' a command
	execRequest := p.RESTClient.Post().
		Resource("pods").
		Name(opts.PodName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", opts.ContainerName)
	execRequest.VersionedParams(&apiv1.PodExecOptions{
		Container: opts.ContainerName,
		Command:   opts.Command,
		Stdin:     opts.In != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)
	// Create an executor to send commands / receive output.
	// SPDY Allows multiplexed bidirectional streams to and from  the pod
	exec, err := remotecommand.NewSPDYExecutor(p.clusterConfig, "POST", execRequest.URL())
	if err != nil {
		return "", "", fmt.Errorf("Error creating SPDYExecutor: %v", err)
	}
	// Execute the command, with Std(in,out,err) pointing to the
	// above readers and writers
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  opts.In,
		Stdout: opts.Out,
		Stderr: opts.Err,
		Tty:    false,
	})
	if err != nil {
		return "", "", fmt.Errorf("Error executing remote command: %v", err)
	}
	return "", "", nil
}

// ExecInPod is a convenience function to call ExecCmd without needing to set up writers for stdOut/Err
// Accepts a pod name, container name, namespace and command. If you want to pass StdIn have a need for a
//reader / writer then you'll need to use ExecCmd. If the command executes in the container and stdErr is
// not nil, the command will return an error containing the contents of stdErr
func (p *Kubernetesp) ExecInPod(podName string, containerName, namespace string, command []string) (string, string, error) {
	stdOut := new(Writer)
	stdErr := new(Writer)
	opts := &CmdOptions{
		Command: command,
		StreamOptions: StreamOptions{
			PodName:       podName,
			ContainerName: containerName,
			Out:           stdOut,
			Err:           stdErr,
		},
	}

	// Exec the command in the pod
	_, _, err := p.ExecCmd(opts, namespace)
	if err != nil {
		return "", "", err
	}
	if len(stdErr.Str) != 0 {
		return strings.Join(stdOut.Str, " "), strings.Join(stdErr.Str, " "), fmt.Errorf("Command returned non zero: %s", stdErr.Str)
	}

	return strings.Join(stdOut.Str, " "), strings.Join(stdErr.Str, " "), nil

}

// PortForward sets up port forwarding to the manager that is running inside the cluster
func (p *Kubernetesp) PortForward(pod *apiv1.Pod, requiredPorts []int) error {
	if pod.Status.Phase != apiv1.PodRunning {
		p.Logger.Error("unable to forward port because pod is not running.", "status", pod.Status.Phase, "pod", pod.ObjectMeta.Name)
		return fmt.Errorf("unable to forward port because pod is not running. Current status=%v", pod.Status.Phase)
	}

	req := p.RESTClient.Post().
		Resource("pods").
		Namespace(p.NewNamespaceName).
		Name(pod.Name).
		SubResource("portforward")

	// convert ports from []int to []string
	ports := make([]string, 2)
	for i, port := range requiredPorts {
		ports[i] = strconv.Itoa(port)
	}
	fmt.Println(ports)
	fmt.Println("returning at end of portForward")

	return p.forwardPorts("POST", req.URL(), ports)

}

func (p *Kubernetesp) forwardPorts(method string, url *url.URL, requiredPorts []string) error {
	fmt.Println("In ForwardPorts")
	transport, upgrader, err := spdy.RoundTripperFor(p.clusterConfig)
	if err != nil {
		return err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, method, url)
	fw, err := portforward.New(dialer, requiredPorts, p.StopChannel, p.ReadyChannel, p.cmdOut, p.cmdErr)
	if err != nil {
		return err
	}

	return fw.ForwardPorts()
}

// CopyTar copies the files defined in each filePair in files to the pod provided.
// Called by controller when initContainer status is running.
func (p *Kubernetesp) CopyTar(files []FilePair, pod *apiv1.Pod) error {
	p.Logger.Info(fmt.Sprintf("copyTar Called with files %#v on pod %s", files, pod.ObjectMeta.Name))
	//Set up new pipe
	pipeReader, pipeWriter := io.Pipe()

	var err error
	go func() {
		defer pipeWriter.Close()
		tarErr := makeTar(files, pipeWriter)
		if tarErr != nil {
			p.Logger.Error("error writing tar", "err", tarErr)
			err = tarErr
		}
	}()
	if err != nil {
		return err
	}

	stdOut := new(Writer)
	stdErr := new(Writer)

	opts := &CmdOptions{
		StreamOptions: StreamOptions{
			PodName:       pod.ObjectMeta.Name,
			ContainerName: pod.Spec.InitContainers[0].Name,
			In:            pipeReader,
			Out:           stdOut,
			Err:           stdErr,
		},
	}

	_, _, err = p.AttachCmd(opts)
	if err != nil {
		p.Logger.Error("error running AttachCmd for CopyTar", "err", err)
	}

	p.Logger.Debug(fmt.Sprintf("contents of stdOut: %v\n", stdOut.Str))
	p.Logger.Debug(fmt.Sprintf("contents of stdErr: %v\n", stdErr.Str))
	return err

}

// GetLog Gets the logs from a container with the name 'wr-runner'
// Returns the last n lines.
func (p *Kubernetesp) GetLog(pod *apiv1.Pod, lines int) (string, error) {
	req := p.RESTClient.Get().
		Namespace(p.NewNamespaceName).
		Name(pod.ObjectMeta.Name).
		Resource("pods").
		SubResource("log").
		Param("container", "wr-runner").
		Param("tailLines", fmt.Sprintf("%v", lines))

	readCloser, err := req.Stream()
	if err != nil {
		return "", err
	}

	out := new(Writer)

	defer readCloser.Close()
	_, err = io.Copy(out, readCloser)
	if err != nil {
		return "", err
	}

	return strings.Join(out.Str, " "), nil
}
