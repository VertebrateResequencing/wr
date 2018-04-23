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

// This file contains the code for the Pod struct.

import (
	"os/signal"

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

type Pod struct {
	ID        string
	Name      string
	Resources *ResourceRequest
	logger    log15.Logger
}

type ResourceRequest struct {
	Default bool
	Cores   int
	Disk    int
	Ram     int
}

type filePair struct {
	src, dest string
}

// Writer provides a method for writing output (from stderr)
type Writer struct {
	Str []string
}

type portForwarder interface {
	forwardPorts(method string, url *url.URL, requiredPorts []string) error
	PortForward(podName string, requiredPorts []int) error
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
// Takes a slice of filePair(s), format source, destination
func makeTar(files []filePair, writer io.Writer) error {
	//Set up tar writer
	tarWriter := tar.NewWriter(writer)
	defer tarWriter.Close()
	//Add each file to the tarball
	fmt.Println(len(files))
	for i := range files {
		fmt.Printf("Adding file %v \n", files[i])
		if err := addFile(tarWriter, path.Clean(files[i].src), files[i].dest); err != nil {
			panic(err)
		}
	}
	fmt.Println("Done adding files to tar")
	return nil
}

//TODO: Implement
func (s *Pod) RunCmd(cmd string, background bool) (stdout, stderr string, err error) {
	return "", "", nil
}

func (p *kubernetesp) forwardPorts(method string, url *url.URL, requiredPorts []string) error {
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

// Sets up port forwarding to the manager that is running inside the cluster
func (p *kubernetesp) PortForward(podName string, requiredPorts []int) error {
	fmt.Println(podName)
	pod, err := p.podClient.Get(podName, metav1.GetOptions{})
	if err != nil {
		p.Logger.Error("Getting pod for port forward", "err", err, "pod", podName)
		return err
	}
	fmt.Println(pod)
	if pod.Status.Phase != apiv1.PodRunning {
		p.Logger.Error("unable to forward port because pod is not running.", "status", pod.Status.Phase, "pod", podName)
		return fmt.Errorf("unable to forward port because pod is not running. Current status=%v", pod.Status.Phase)
	}

	signals := make(chan os.Signal, 1)   // channel to receive interrupt
	signal.Notify(signals, os.Interrupt) // Notify on interrupt
	defer signal.Stop(signals)           // stop relaying signals to signals

	// Avoid deadlock using goroutine
	go func() {
		<-signals
		if p.StopChannel != nil {
			close(p.StopChannel)
		}
	}()

	req := p.RESTClient.Post().
		Resource("pods").
		Namespace(p.newNamespaceName).
		Name(pod.Name).
		SubResource("portforward")

	//convert ports from []int to []string
	ports := make([]string, 2)
	for i, port := range requiredPorts {
		ports[i] = strconv.Itoa(port)
	}
	fmt.Println(ports)
	fmt.Println("returning at end of portForward")

	return p.forwardPorts("POST", req.URL(), ports)

}
