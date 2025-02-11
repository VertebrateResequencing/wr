/*******************************************************************************
 * Copyright (c) 2021, 2025 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package testing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/phayes/freeport"
)

const (
	userOnlyPerm         = 0700
	serverTimeout        = 10 * time.Second
	serverRetryFrequency = 500 * time.Millisecond
)

// PrepareWrConfig creates a temp directory, changes to that directory, creates
// a wr config file with available ports set, then returns a ServerConfig with
// that configuration. It also returns a function you should defer, which
// changes directory back.
func PrepareWrConfig(t *testing.T) (jobqueue.ServerConfig, func()) {
	t.Helper()

	clientPort, webPort := getPorts(t)
	dir, managerDir, managerDirActual, d := prepareDir(t)
	config := jobqueue.ServerConfig{
		Port:            strconv.Itoa(clientPort),
		WebPort:         strconv.Itoa(webPort),
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: "bash"},
		DBFile:          filepath.Join(managerDirActual, "db"),
		DBFileBackup:    filepath.Join(managerDirActual, "db_bk"),
		TokenFile:       filepath.Join(managerDirActual, "client.token"),
		CAFile:          filepath.Join(managerDirActual, "ca.pem"),
		CertFile:        filepath.Join(managerDirActual, "cert.pem"),
		CertDomain:      "localhost",
		KeyFile:         filepath.Join(managerDirActual, "key.pem"),
		Deployment:      "development",
	}

	writeConfig(t, dir, managerDir, config)

	return config, d
}

func getPorts(t *testing.T) (int, int) {
	t.Helper()

	clientPort, err := freeport.GetFreePort()
	if err != nil {
		t.Fatalf("getting free port failed: %s", err)
	}

	webPort, err := freeport.GetFreePort()
	if err != nil {
		t.Fatalf("getting free port failed: %s", err)
	}

	return clientPort, webPort
}

func prepareDir(t *testing.T) (string, string, string, func()) {
	t.Helper()

	dir, d := CDTmpDir(t)
	managerDir := filepath.Join(dir, ".wr")
	managerDirActual := managerDir + "_development"

	err := os.MkdirAll(managerDirActual, userOnlyPerm)
	if err != nil {
		t.Fatal(err)
	}

	return dir, managerDir, managerDirActual, d
}

// CDTmpDir changes directory to a temp directory. It returns the path to the
// temp dir and a function you should defer to change back to your original
// directory. The tmp dir will be automatically deleted when tests end.
func CDTmpDir(t *testing.T) (string, func()) {
	t.Helper()

	tmpDir := t.TempDir()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	d := func() {
		err = os.Chdir(cwd)
		if err != nil {
			t.Logf("Chdir failed: %s", err)
		}
	}

	return tmpDir, d
}

func writeConfig(t *testing.T, dir, managerDir string, config jobqueue.ServerConfig) {
	t.Helper()

	f, err := os.Create(filepath.Join(dir, ".wr_config.yml"))
	if err != nil {
		t.Fatal(err)
	}

	configData := `managerport: "%s"
managerweb: "%s"
managerdir: "%s"`

	_, err = f.WriteString(fmt.Sprintf(configData, config.Port, config.WebPort, managerDir))
	if err != nil {
		t.Fatal(err)
	}
}

// Serve calls jobqueue.Serve() but with a retry for 5s on failure. This allows
// time for a server that we recently stopped in a prior test to really not be
// listening on the ports any more.
func Serve(t *testing.T, config jobqueue.ServerConfig) *jobqueue.Server {
	t.Helper()

	server, _, _, err := jobqueue.Serve(context.Background(), config)
	if err != nil {
		server, err = serveWithRetries(t, config)
	}

	if err != nil {
		t.Fatal(err)
	}

	return server
}

// serveWithRetries does the retrying part of serve().
func serveWithRetries(t *testing.T, config jobqueue.ServerConfig) (server *jobqueue.Server, err error) {
	t.Helper()

	limit := time.After(serverTimeout)
	ticker := time.NewTicker(serverRetryFrequency)

	for {
		select {
		case <-ticker.C:
			server, _, _, err = jobqueue.Serve(context.Background(), config)
			if err != nil {
				continue
			}

			ticker.Stop()

			return server, err
		case <-limit:
			ticker.Stop()

			return server, err
		}
	}
}
