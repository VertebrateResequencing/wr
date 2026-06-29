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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	imageimages "github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	testOpenStackInterfacePath = "/servers/server-id/os-interface"
	testOpenStackNetworkID     = "network-id"
	testOpenStackNetworkName   = "wr-testing"
	testOpenStackPortID        = "port-id"
	testOpenStackServerID      = "server-id"
	testOpenStackServerIPQuery = "ip_address=192.168.0.12"
)

func TestOpenStackCreateServerRetriesTransientFailures(t *testing.T) {
	Convey("OpenStack server creation retries a transient 500 before succeeding", t, func() {
		var attempts atomic.Int32

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := attempts.Add(1)
			if attempt == 1 {
				http.Error(w, "transient nova/neutron fault", http.StatusInternalServerError)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)

			if _, err := io.WriteString(w, `{"server":{"id":"server-id","adminPass":"secret"}}`); err != nil {
				t.Errorf("write create response: %s", err)
			}
		}))
		defer api.Close()

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(api.URL),
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
		}

		server, _, createdVolume, err := provider.createServer(context.Background(), testResources(),
			&imageimages.Image{ID: "image-id"}, "flavor-id", &Flavor{Disk: 1}, 1)

		So(err, ShouldBeNil)
		So(server.ID, ShouldEqual, testOpenStackServerID)
		So(createdVolume, ShouldBeFalse)
		So(attempts.Load(), ShouldEqual, int32(2))
	})
}

func TestOpenStackCreateAndWaitRetriesCreatedServerThatNeverBecomesVisible(t *testing.T) {
	Convey("OpenStack server creation retries when an accepted server never becomes visible", t, func() {
		var (
			creates      atomic.Int32
			quotaSignals atomic.Int32
			waits        atomic.Int32
		)

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempt := creates.Add(1)

			serverID := "ghost-server-id"
			if attempt == 2 {
				serverID = testOpenStackServerID
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)

			if _, err := io.WriteString(w, `{"server":{"id":"`+serverID+`","adminPass":"secret"}}`); err != nil {
				t.Errorf("write create response: %s", err)
			}
		}))
		defer api.Close()

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(api.URL),
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
		}

		usingQuotaCh := make(chan bool)
		quotaDone := make(chan struct{})

		go func() {
			defer close(quotaDone)

			for range usingQuotaCh {
				quotaSignals.Add(1)
			}
		}()

		waitForActive := func(_ context.Context, _ *servers.Server, serverID string, _ bool) error {
			waits.Add(1)

			if serverID == "ghost-server-id" {
				return gophercloud.ErrUnexpectedResponseCode{Actual: http.StatusNotFound}
			}

			return nil
		}

		server, _, err := provider.createAndWaitForServerWithWait(context.Background(), testResources(),
			&imageimages.Image{ID: "image-id"}, "flavor-id", &Flavor{Disk: 1}, 1, usingQuotaCh, waitForActive)

		close(usingQuotaCh)
		<-quotaDone

		So(err, ShouldBeNil)
		So(server.ID, ShouldEqual, testOpenStackServerID)
		So(creates.Load(), ShouldEqual, int32(2))
		So(waits.Load(), ShouldEqual, int32(2))
		So(quotaSignals.Load(), ShouldEqual, int32(1))
	})
}

func TestOpenStackPollServerStatusRetriesInitialNotFound(t *testing.T) {
	Convey("OpenStack server status polling retries while a new server is not visible yet", t, func() {
		var gets atomic.Int32

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := gets.Add(1)
			if attempt == 1 {
				http.Error(w, "server not indexed yet", http.StatusNotFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			if _, err := io.WriteString(w, `{"server":{"id":"server-id","status":"ACTIVE"}}`); err != nil {
				t.Errorf("write server response: %s", err)
			}
		}))
		defer api.Close()

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(api.URL),
		}
		provider.initSpawnTracking()

		start := time.Now()
		done, err := provider.pollServerStatusTick(context.Background(), testOpenStackServerID, false, start, 1)

		So(err, ShouldBeNil)
		So(done, ShouldBeFalse)

		done, err = provider.pollServerStatusTick(context.Background(), testOpenStackServerID, false, start, 2)

		So(err, ShouldBeNil)
		So(done, ShouldBeTrue)
		So(gets.Load(), ShouldEqual, int32(2))
	})
}

func TestOpenStackPollServerStatusRetriesExtendedInitialNotFound(t *testing.T) {
	Convey("OpenStack server status polling keeps retrying 404s within the normal spawn window", t, func() {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "server still not indexed yet", http.StatusNotFound)
		}))
		defer api.Close()

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(api.URL),
		}

		done, err := provider.pollServerStatusTick(context.Background(), testOpenStackServerID, false, time.Now(), 31)

		So(err, ShouldBeNil)
		So(done, ShouldBeFalse)
	})
}

func TestOpenStackGetServerPortIDRetriesDelayedVisibility(t *testing.T) {
	Convey("OpenStack server port lookup retries while a new server port is not visible yet", t, func() {
		var lists atomic.Int32

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method: %s", r.Method)
			}

			if r.URL.Path != "/ports" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			if r.URL.Query().Get("device_id") != testOpenStackServerID {
				t.Errorf("unexpected device_id: %s", r.URL.Query().Get("device_id"))
			}

			if r.URL.Query().Get("network_id") != "" {
				t.Errorf("unexpected network_id: %s", r.URL.Query().Get("network_id"))
			}

			w.Header().Set("Content-Type", "application/json")

			attempt := lists.Add(1)
			if attempt == 1 {
				if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
					t.Errorf("write empty ports response: %s", err)
				}

				return
			}

			if _, err := io.WriteString(w, `{
				"ports": [{
					"id": "port-id",
					"network_id": "network-id",
					"device_id": "server-id",
					"fixed_ips": [{"ip_address": "192.168.0.12"}]
				}]
			}`); err != nil {
				t.Errorf("write port response: %s", err)
			}
		}))
		defer api.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			networkClient: fakeOpenStackNetworkClient(api.URL),
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		portID, err := provider.getServerPortID(context.Background(), testOpenStackServerID)

		So(err, ShouldBeNil)
		So(portID, ShouldEqual, testOpenStackPortID)
		So(lists.Load(), ShouldEqual, int32(2))
	})
}

func TestOpenStackGetServerPortIDUsesComputeInterfaceWhenPortListIsEmpty(t *testing.T) {
	Convey("OpenStack server port lookup can use Nova interface details before Neutron lists the port", t, func() {
		var (
			interfaceLists atomic.Int32
			portLists      atomic.Int32
		)

		networkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			portLists.Add(1)
			w.Header().Set("Content-Type", "application/json")

			if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
				t.Errorf("write empty ports response: %s", err)
			}
		}))
		defer networkAPI.Close()

		computeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			interfaceLists.Add(1)

			if r.Method != http.MethodGet {
				t.Errorf("unexpected method: %s", r.Method)
			}

			if r.URL.Path != testOpenStackInterfacePath {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			w.Header().Set("Content-Type", "application/json")

			if _, err := io.WriteString(w, `{
				"interfaceAttachments": [{
					"port_id": "port-id",
					"net_id": "network-id",
					"fixed_ips": [{"ip_address": "192.168.0.12"}]
				}]
			}`); err != nil {
				t.Errorf("write interface response: %s", err)
			}
		}))
		defer computeAPI.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(computeAPI.URL),
			networkClient: fakeOpenStackNetworkClient(networkAPI.URL),
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		portID, err := provider.getServerPortID(ctx, testOpenStackServerID)

		So(err, ShouldBeNil)
		So(portID, ShouldEqual, testOpenStackPortID)
		So(portLists.Load(), ShouldEqual, int32(1))
		So(interfaceLists.Load(), ShouldEqual, int32(1))
	})
}

func TestOpenStackGetServerPortIDUsesServerFixedIPWhenPortAndInterfaceListsAreEmpty(t *testing.T) {
	Convey("OpenStack server port lookup can use server address details when port and interface lists lag", t, func() {
		var (
			devicePortLists  atomic.Int32
			fixedIPPortLists atomic.Int32
			addressLists     atomic.Int32
			interfaceLists   atomic.Int32
		)

		networkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method: %s", r.Method)
			}

			if r.URL.Path != "/ports" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			w.Header().Set("Content-Type", "application/json")

			switch {
			case r.URL.Query().Get("device_id") == testOpenStackServerID:
				devicePortLists.Add(1)

				if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
					t.Errorf("write empty ports response: %s", err)
				}
			case r.URL.Query().Get("fixed_ips") == testOpenStackServerIPQuery:
				fixedIPPortLists.Add(1)

				if _, err := io.WriteString(w, `{
					"ports": [{
						"id": "port-id",
						"network_id": "metadata-lag-network-id",
						"device_id": "server-id",
						"fixed_ips": [{"ip_address": "192.168.0.12"}]
					}]
				}`); err != nil {
					t.Errorf("write fixed IP port response: %s", err)
				}
			default:
				t.Errorf("unexpected port query: %s", r.URL.RawQuery)
			}
		}))
		defer networkAPI.Close()

		computeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case testOpenStackInterfacePath:
				interfaceLists.Add(1)

				if _, err := io.WriteString(w, `{"interfaceAttachments":[]}`); err != nil {
					t.Errorf("write empty interface response: %s", err)
				}
			case "/servers/server-id/ips/" + testOpenStackNetworkName:
				addressLists.Add(1)

				if _, err := io.WriteString(w, `{
					"`+testOpenStackNetworkName+`": [
						{"version": 6, "addr": "2001:db8::12"},
						{"version": 4, "addr": "192.168.0.12"}
					]
				}`); err != nil {
					t.Errorf("write address response: %s", err)
				}
			default:
				t.Errorf("unexpected compute path: %s", r.URL.Path)
			}
		}))
		defer computeAPI.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(computeAPI.URL),
			networkClient: fakeOpenStackNetworkClient(networkAPI.URL),
			networkName:   testOpenStackNetworkName,
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		portID, err := provider.getServerPortID(ctx, testOpenStackServerID)

		So(err, ShouldBeNil)
		So(portID, ShouldEqual, testOpenStackPortID)
		So(devicePortLists.Load(), ShouldEqual, int32(1))
		So(interfaceLists.Load(), ShouldEqual, int32(1))
		So(addressLists.Load(), ShouldEqual, int32(1))
		So(fixedIPPortLists.Load(), ShouldEqual, int32(1))
	})
}

func TestOpenStackGetServerPortIDUsesServerDetailsAddressWhenNamedNetworkAddressesAreEmpty(t *testing.T) {
	Convey("OpenStack server port lookup can use server details when named network addresses lag", t, func() {
		var (
			allAddressLists   atomic.Int32
			devicePortLists   atomic.Int32
			fixedIPPortLists  atomic.Int32
			interfaceLists    atomic.Int32
			namedAddressLists atomic.Int32
			serverGets        atomic.Int32
		)

		networkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch {
			case r.URL.Query().Get("device_id") == testOpenStackServerID:
				devicePortLists.Add(1)

				if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
					t.Errorf("write empty ports response: %s", err)
				}
			case r.URL.Query().Get("fixed_ips") == testOpenStackServerIPQuery:
				fixedIPPortLists.Add(1)

				if _, err := io.WriteString(w, `{
					"ports": [{
						"id": "port-id",
						"network_id": "metadata-lag-network-id",
						"fixed_ips": [{"ip_address": "192.168.0.12"}]
					}]
				}`); err != nil {
					t.Errorf("write fixed IP port response: %s", err)
				}
			default:
				t.Errorf("unexpected port query: %s", r.URL.RawQuery)
			}
		}))
		defer networkAPI.Close()

		computeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case testOpenStackInterfacePath:
				interfaceLists.Add(1)

				if _, err := io.WriteString(w, `{"interfaceAttachments":[]}`); err != nil {
					t.Errorf("write empty interface response: %s", err)
				}
			case "/servers/server-id/ips/" + testOpenStackNetworkName:
				namedAddressLists.Add(1)

				if _, err := io.WriteString(w, `{"`+testOpenStackNetworkName+`":[]}`); err != nil {
					t.Errorf("write empty named address response: %s", err)
				}
			case "/servers/server-id/ips":
				allAddressLists.Add(1)

				if _, err := io.WriteString(w, `{"addresses":{"`+testOpenStackNetworkName+`":[]}}`); err != nil {
					t.Errorf("write empty address response: %s", err)
				}
			case "/servers/server-id":
				serverGets.Add(1)

				if _, err := io.WriteString(w, `{
					"server": {
						"id": "server-id",
						"status": "ACTIVE",
						"addresses": {
							"metadata-lag-network": [{"version": 4, "addr": "192.168.0.12"}]
						}
					}
				}`); err != nil {
					t.Errorf("write server response: %s", err)
				}
			default:
				t.Errorf("unexpected compute path: %s", r.URL.Path)
			}
		}))
		defer computeAPI.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(computeAPI.URL),
			networkClient: fakeOpenStackNetworkClient(networkAPI.URL),
			networkName:   testOpenStackNetworkName,
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		portID, err := provider.getServerPortID(ctx, testOpenStackServerID)

		So(err, ShouldBeNil)
		So(portID, ShouldEqual, testOpenStackPortID)
		So(devicePortLists.Load(), ShouldEqual, int32(1))
		So(interfaceLists.Load(), ShouldEqual, int32(1))
		So(namedAddressLists.Load(), ShouldEqual, int32(1))
		So(allAddressLists.Load(), ShouldEqual, int32(1))
		So(serverGets.Load(), ShouldEqual, int32(1))
		So(fixedIPPortLists.Load(), ShouldEqual, int32(1))
	})
}

func TestOpenStackGetServerPortIDRejectsAmbiguousServerFixedIPPorts(t *testing.T) {
	Convey("OpenStack server port lookup does not guess when a fixed IP lookup returns multiple ports", t, func() {
		networkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch {
			case r.URL.Query().Get("device_id") == testOpenStackServerID:
				if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
					t.Errorf("write empty ports response: %s", err)
				}
			case r.URL.Query().Get("fixed_ips") == testOpenStackServerIPQuery:
				if _, err := io.WriteString(w, `{
					"ports": [
						{
							"id": "first-port-id",
							"network_id": "first-network-id",
							"fixed_ips": [{"ip_address": "192.168.0.12"}]
						},
						{
							"id": "second-port-id",
							"network_id": "second-network-id",
							"fixed_ips": [{"ip_address": "192.168.0.12"}]
						}
					]
				}`); err != nil {
					t.Errorf("write ambiguous ports response: %s", err)
				}
			default:
				t.Errorf("unexpected port query: %s", r.URL.RawQuery)
			}
		}))
		defer networkAPI.Close()

		computeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case testOpenStackInterfacePath:
				if _, err := io.WriteString(w, `{"interfaceAttachments":[]}`); err != nil {
					t.Errorf("write empty interface response: %s", err)
				}
			case "/servers/server-id/ips/" + testOpenStackNetworkName:
				if _, err := io.WriteString(w, `{
					"`+testOpenStackNetworkName+`": [{"version": 4, "addr": "192.168.0.12"}]
				}`); err != nil {
					t.Errorf("write address response: %s", err)
				}
			default:
				t.Errorf("unexpected compute path: %s", r.URL.Path)
			}
		}))
		defer computeAPI.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			computeClient: fakeOpenStackComputeClient(computeAPI.URL),
			networkClient: fakeOpenStackNetworkClient(networkAPI.URL),
			networkName:   testOpenStackNetworkName,
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		portID, err := provider.getServerPortIDOnce(context.Background(), testOpenStackServerID)

		var multipleErr gophercloud.ErrMultipleResourcesFound
		So(errors.As(err, &multipleErr), ShouldBeTrue)
		So(multipleErr.Name, ShouldEqual, "192.168.0.12")
		So(multipleErr.Count, ShouldEqual, 2)
		So(multipleErr.ResourceType, ShouldEqual, serverPortResourceType)
		So(portID, ShouldBeBlank)
	})
}

func TestOpenStackGetServerPortIDStopsRetryingWhenContextCancelled(t *testing.T) {
	Convey("OpenStack server port lookup stops waiting when its context is cancelled", t, func() {
		var lists atomic.Int32

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			lists.Add(1)

			w.Header().Set("Content-Type", "application/json")

			if _, err := io.WriteString(w, `{"ports":[]}`); err != nil {
				t.Errorf("write empty ports response: %s", err)
			}
		}))
		defer api.Close()

		_, ipNet, err := net.ParseCIDR("192.168.0.0/24")
		So(err, ShouldBeNil)

		provider := &openstackp{
			networkClient: fakeOpenStackNetworkClient(api.URL),
			networks:      []servers.Network{{UUID: testOpenStackNetworkID}},
			ipNet:         ipNet,
		}

		cancelTimer := time.AfterFunc(10*time.Millisecond, cancel)
		defer cancelTimer.Stop()

		portID, err := provider.getServerPortID(ctx, testOpenStackServerID)

		So(err, ShouldEqual, context.Canceled)
		So(portID, ShouldBeBlank)
		So(lists.Load(), ShouldEqual, int32(1))
	})
}

func fakeOpenStackNetworkClient(endpoint string) *gophercloud.ServiceClient {
	return fakeOpenStackComputeClient(endpoint)
}

func fakeOpenStackComputeClient(endpoint string) *gophercloud.ServiceClient {
	return &gophercloud.ServiceClient{
		ProviderClient: &gophercloud.ProviderClient{},
		Endpoint:       endpoint + "/",
	}
}

func testResources() *Resources {
	return &Resources{
		ResourceName: testOpenStackNetworkName,
		Details:      map[string]string{},
		Servers:      map[string]*Server{},
	}
}
