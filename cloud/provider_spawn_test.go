/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
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

package cloud

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	fakeProviderName         = "fake"
	spawnBeforeQuotaFlavorID = "tiny"
)

var errSpawnBeforeQuota = errors.New("failed before using quota")

type spawnBeforeQuotaErrorProvider struct{}

func (p *spawnBeforeQuotaErrorProvider) requiredEnv() []string {
	return nil
}

func (p *spawnBeforeQuotaErrorProvider) maybeEnv() []string {
	return nil
}

func (p *spawnBeforeQuotaErrorProvider) initialize() error {
	return nil
}

func (p *spawnBeforeQuotaErrorProvider) deploy(ctx context.Context, resources *Resources, requiredPorts []int,
	useConfigDrive bool, gatewayIP, cidr string, dnsNameServers []string,
) error {
	return nil
}

func (p *spawnBeforeQuotaErrorProvider) inCloud(ctx context.Context) bool {
	return false
}

func (p *spawnBeforeQuotaErrorProvider) getCurrentServers(resources *Resources) ([][]string, error) {
	return [][]string{}, nil
}

func (p *spawnBeforeQuotaErrorProvider) getQuota(ctx context.Context) (*Quota, error) {
	return &Quota{}, nil
}

func (p *spawnBeforeQuotaErrorProvider) flavors(ctx context.Context) map[string]*Flavor {
	return map[string]*Flavor{
		spawnBeforeQuotaFlavorID: {
			ID:    spawnBeforeQuotaFlavorID,
			Name:  spawnBeforeQuotaFlavorID,
			Cores: 2,
			RAM:   1024,
			Disk:  10,
		},
	}
}

func (p *spawnBeforeQuotaErrorProvider) spawn(ctx context.Context, resources *Resources, os string, flavor string,
	diskGB int, externalIP bool, usingQuotaCh chan bool,
) (serverID, serverIP, serverName, adminPass string, err error) {
	return "", "", "", "", errSpawnBeforeQuota
}

func (p *spawnBeforeQuotaErrorProvider) errIsNoHardware(err error) bool {
	return false
}

func (p *spawnBeforeQuotaErrorProvider) checkServer(serverID string) (bool, error) {
	return false, nil
}

func (p *spawnBeforeQuotaErrorProvider) serverIsKnown(serverID string) (bool, error) {
	return false, nil
}

func (p *spawnBeforeQuotaErrorProvider) destroyServer(ctx context.Context, serverID string) error {
	return nil
}

func (p *spawnBeforeQuotaErrorProvider) tearDown(ctx context.Context, resources *Resources) error {
	return nil
}

func TestProviderSpawnCallsUsingQuotaCallbackOnEarlyError(t *testing.T) {
	Convey("Provider Spawn calls the using-quota callback when spawn returns before using quota", t, func() {
		p := &Provider{
			impl: &spawnBeforeQuotaErrorProvider{},
			Name: fakeProviderName,
		}
		called := 0

		server, err := p.Spawn(
			context.Background(), "missing-os", "ubuntu", spawnBeforeQuotaFlavorID, 20, time.Minute, false,
			func() {
				called++
			},
		)

		So(server, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(called, ShouldEqual, 1)
	})
}

type teardownDuringSpawnProvider struct {
	spawnBeforeQuotaErrorProvider

	releaseSpawn    chan struct{}
	spawnEntered    chan struct{}
	tearDownEntered chan struct{}
}

func newTeardownDuringSpawnProvider() *teardownDuringSpawnProvider {
	return &teardownDuringSpawnProvider{
		releaseSpawn:    make(chan struct{}),
		spawnEntered:    make(chan struct{}),
		tearDownEntered: make(chan struct{}),
	}
}

func (p *teardownDuringSpawnProvider) spawn(ctx context.Context, resources *Resources, os string, flavor string,
	diskGB int, externalIP bool, usingQuotaCh chan bool,
) (serverID, serverIP, serverName, adminPass string, err error) {
	close(p.spawnEntered)
	<-p.releaseSpawn

	return "server-id", "192.0.2.1", "server-name", "", nil
}

func (p *teardownDuringSpawnProvider) tearDown(ctx context.Context, resources *Resources) error {
	close(p.tearDownEntered)

	resources.PrivateKey = ""

	return nil
}

func TestProviderTearDownWaitsForInFlightSpawn(t *testing.T) {
	Convey("Provider TearDown waits for an in-flight Spawn before mutating resources", t, func() {
		privateKey := "private-key"
		fakeProvider := newTeardownDuringSpawnProvider()
		provider := &Provider{
			impl: fakeProvider,
			Name: fakeProviderName,
			resources: &Resources{
				ResourceName: "resource-name",
				Details:      map[string]string{},
				PrivateKey:   privateKey,
				Servers:      map[string]*Server{},
			},
			savePath: filepath.Join(t.TempDir(), "resources"),
			servers:  map[string]*Server{},
		}

		spawnResult := make(chan providerSpawnResult, 1)

		go func() {
			server, err := provider.Spawn(
				context.Background(), "linux", "ubuntu", spawnBeforeQuotaFlavorID, 20, time.Minute, false,
			)
			spawnResult <- providerSpawnResult{server: server, err: err}
		}()

		<-fakeProvider.spawnEntered

		tearDownErr := make(chan error, 1)
		go func() {
			tearDownErr <- provider.TearDown(context.Background())
		}()

		tearDownStarted := false

		select {
		case <-fakeProvider.tearDownEntered:
			tearDownStarted = true
		case <-time.After(50 * time.Millisecond):
		}

		So(tearDownStarted, ShouldBeFalse)

		close(fakeProvider.releaseSpawn)

		result := <-spawnResult
		So(result.err, ShouldBeNil)
		So(result.server, ShouldNotBeNil)
		So(result.server.PrivateKey, ShouldEqual, privateKey)
		So(<-tearDownErr, ShouldBeNil)
		So(provider.PrivateKey(), ShouldBeBlank)
	})
}

type providerSpawnResult struct {
	server *Server
	err    error
}

type destroyContextKey struct{}

func TestServerDestroyUsesDetachedProviderContext(t *testing.T) {
	Convey("Server Destroy still asks the provider to destroy when caller context is cancelled", t, func() {
		serverID := "server-to-destroy"
		fakeProvider := &destroyAfterCancelProvider{}
		provider := &Provider{
			impl:      fakeProvider,
			Name:      fakeProviderName,
			resources: &Resources{Servers: map[string]*Server{serverID: nil}},
			savePath:  filepath.Join(t.TempDir(), "resources"),
		}
		server := &Server{
			ID:           serverID,
			provider:     provider,
			cancelRunCmd: make(map[int]chan bool),
		}

		ctx := context.WithValue(context.Background(), destroyContextKey{}, "preserved")
		deadline := time.Now().Add(time.Hour)

		ctx, cancelDeadline := context.WithDeadline(ctx, deadline)
		defer cancelDeadline()

		ctx, cancel := context.WithCancel(ctx)
		cancel()

		err := server.Destroy(ctx)

		So(err, ShouldBeNil)
		So(fakeProvider.destroyedServerID, ShouldEqual, serverID)
		So(fakeProvider.destroyCtxErr, ShouldBeNil)
		So(fakeProvider.destroyCtxValue, ShouldEqual, "preserved")
		So(fakeProvider.destroyCtxDeadline, ShouldResemble, deadline)
	})
}

type destroyAfterCancelProvider struct {
	spawnBeforeQuotaErrorProvider

	destroyCtxErr      error
	destroyCtxValue    any
	destroyCtxDeadline time.Time
	destroyedServerID  string
}

func (p *destroyAfterCancelProvider) destroyServer(ctx context.Context, serverID string) error {
	p.destroyCtxErr = ctx.Err()
	p.destroyCtxValue = ctx.Value(destroyContextKey{})
	p.destroyCtxDeadline, _ = ctx.Deadline()
	p.destroyedServerID = serverID

	return p.destroyCtxErr
}

func (p *destroyAfterCancelProvider) checkServer(serverID string) (bool, error) {
	return true, nil
}
