// Below is roughly how I invisage this getting used.
// Will deploy wr-manager to cluster, start up running
// the provided script, followed by wr. Then port forwards
// and daemonises.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/VertebrateResequencing/wr/kubernetes/client"
	"github.com/VertebrateResequencing/wr/kubernetes/deployment"
	"github.com/inconshreveable/log15"
	"github.com/sb10/l15h"
	"github.com/sevlyar/go-daemon"
)

var (
	signal = flag.String("s", "", `send signal to the port forwarding daemon
		quit -- graceful shutdown 
		stop -- fast shutdown`)
	script     = flag.String("script", "", `postcreation script to be used`)
	binary     = flag.String("binary", "", `path to wr binary`)
	foreground = flag.Bool("f", false, `run in foreground`)
)

var err error
var stopCh chan struct{}
var kubeLogger log15.Logger

func StartController(binaryPath string, scriptPath string, stopCh chan struct{}) {
	log.Printf("controller started with binary path %s and script path %s", binaryPath, scriptPath)
	// Always authenticate the client lib with cluster
	c := deployment.Controller{
		Client: &client.Kubernetesp{},
	}
	c.Clientset, c.Restconfig, err = c.Client.Authenticate() // Authenticate and populate Kubernetesp with clientset and restconfig.
	if err != nil {
		panic(err)
	}
	err = c.Client.Initialize(c.Clientset) // Populate the rest of Kubernetesp
	if err != nil {
		panic(err)
	}
	log.Println("Authenticated and Initialised!")
	log.Println("====================")

	scriptName := filepath.Base(scriptPath)
	configMapName := strings.TrimSuffix(scriptName, filepath.Ext(scriptName))

	// Create a ConfigMap
	err = c.Client.CreateInitScriptConfigMapFromFile(configMapName, scriptPath)
	if err != nil {
		panic(err)
	}
	// Set up the parameters for the deployment
	// AttachCmdOpts gets populated by controller when pod is created.
	dir, err := os.Getwd()
	log.Println(dir)
	if err != nil {
		panic(err)
	}

	c.Opts = &deployment.DeployOpts{
		ContainerImage: "ubuntu:latest",
		TempMountPath:  "/wr-tmp",
		Files: []client.FilePair{
			{binaryPath, "/wr-tmp/"},
		},
		BinaryPath:      "/scripts/" + scriptName,
		BinaryArgs:      []string{"/wr-tmp/wr-linux", "manager", "start", "-f"},
		ConfigMapName:   configMapName,
		ConfigMountPath: "/scripts",
		RequiredPorts:   []int{1120, 1121},
		Logger:          kubeLogger,
	}

	defer close(stopCh)
	log.Printf("\n\n")
	log.Println("====================")
	log.Printf("\n\n")
	log.Println("Controller started :)")

	c.Run(stopCh)

	return
}

func main() {
	flag.Parse()
	daemon.AddCommand(daemon.StringFlag(signal, "quit"), syscall.SIGQUIT, termHandler)
	daemon.AddCommand(daemon.StringFlag(signal, "stop"), syscall.SIGTERM, termHandler)
	args := os.Args

	bAbs, err := filepath.Abs(*binary)
	if err != nil {
		panic(err)
	}
	sAbs, err := filepath.Abs(*script)
	if err != nil {
		panic(err)
	}

	args = append(args, "--binary")
	args = append(args, bAbs)
	args = append(args, "--script")
	args = append(args, sAbs)

	// Create a logger to handle logging things.
	// for debug purposes, set up logging to STDERR
	kubeLogger = log15.New()
	logLevel := log15.LvlDebug
	kubeLogger.SetHandler(log15.LvlFilterHandler(logLevel, l15h.CallerInfoHandler(log15.StderrHandler)))

	log.Printf("Args passed to me: %s", args)

	if !*foreground {

		cntxt := daemon.Context{
			PidFileName: "pfwpid",
			PidFilePerm: 0644,
			LogFileName: "pfwlog",
			LogFilePerm: 0640,
			WorkDir:     "/",
			Umask:       027,
			Args:        args,
		}

		// Daemon currently running
		if len(daemon.ActiveFlags()) > 0 {
			d, err := cntxt.Search()
			if err != nil {
				log.Fatalln("Unable to send signal to daemon: ", err)
			}
			daemon.SendCommands(d)
			return
		}

		// Check if forward flag is set
		d, err := cntxt.Reborn()
		if err != nil {
			log.Fatalln(err)
		}
		if d != nil {
			log.Printf("This is PostParent()")
			return
		}
		defer cntxt.Release()

		log.Println("======================")
		log.Println("daemon started")
		stopCh = make(chan struct{})
		StartController(bAbs, sAbs, stopCh)
	}
	stopCh = make(chan struct{})
	fmt.Printf("bAbs: %s, sAbs: %s /n", bAbs, sAbs)
	StartController(bAbs, sAbs, stopCh)
}

func termHandler(sig os.Signal) error {
	log.Println("terminating portforward....")
	close(stopCh)
	return daemon.ErrStop
}
