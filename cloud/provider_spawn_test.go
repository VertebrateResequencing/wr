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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const spawnBeforeQuotaFlavorID = "tiny"

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
			Name: "fake",
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
