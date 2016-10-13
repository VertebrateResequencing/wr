// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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

package cloud

// This file contains a provideri implementation for OpenStack

import (
	"errors"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/floatingips"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/secgroups"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/images"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/gophercloud/pagination"
	"os"
	"regexp"
	"strings"
	"time"
)

// openstack only allows certain chars in resource names, so we have a regexp to
// check
var openstackValidResourceNameRegexp = regexp.MustCompile(`^[\w -]+$`)

// openstackEnvs contains the environment variable names we need to connect to
// OpenStack
var openstackEnvs = [...]string{"OS_TENANT_ID", "OS_AUTH_URL", "OS_PASSWORD", "OS_REGION_NAME", "OS_USERNAME"}

// openstack is our implementer of provideri
type openstackp struct {
	computeClient     *gophercloud.ServiceClient
	networkClient     *gophercloud.ServiceClient
	poolName          string
	externalNetworkID string
	fmap              map[string]Flavor
	ownName           string
	networkName       string
	networkUUID       string
	securityGroup     string
}

// requiredEnv returns envs
func (p *openstackp) requiredEnv() []string {
	return openstackEnvs[:]
}

// initialize uses our required environment variables to authenticate with
// OpenStack and create some clients we will use in the other methods
func (p *openstackp) initialize() (err error) {
	// authenticate
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return
	}
	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return
	}

	// make a compute client
	p.computeClient, err = openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return
	}

	// make a network client
	p.networkClient, err = openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{
		//Name:   "neutron", //*** "services can have the same Type but a different Name, which is why [...] Name [is] sometimes needed... but how do I see the available names?
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return
	}

	// we need to know the network pool name *** does this have to be a user
	// input/config option? Or can it be discovered?
	p.poolName = os.Getenv("OS_POOL_NAME") // I made this one up, so we'll default to nova
	if p.poolName == "" {
		p.poolName = "nova"
	}
	p.externalNetworkID, err = networks.IDFromName(p.networkClient, p.poolName)
	if err != nil {
		return
	}

	// get the details of all the possible server flavors
	p.fmap = make(map[string]Flavor)
	pager := flavors.ListDetail(p.computeClient, flavors.ListOpts{})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		flavorList, err := flavors.ExtractFlavors(page)
		if err != nil {
			return false, err
		}

		for _, f := range flavorList {
			p.fmap[f.ID] = Flavor{
				ID:    f.ID,
				Cores: f.VCPUs,
				RAM:   f.RAM,
				Disk:  f.Disk,
			}
		}
		return true, nil
	})

	return
}

// deploy achieves the aims of Deploy().
func (p *openstackp) deploy(resources *Resources, requiredPorts []int) (err error) {
	// the resource name can only contain letters, numbers, underscores,
	// spaces and hyphens
	if !openstackValidResourceNameRegexp.MatchString(resources.ResourceName) {
		err = Error{"openstack", "deploy", ErrBadResourceName}
		return
	}

	// get/create key pair
	kp, err := keypairs.Get(p.computeClient, resources.ResourceName).Extract()
	if err != nil {
		if _, notfound := err.(gophercloud.ErrDefault404); notfound {
			// create a new keypair; if we don't supply our own public key, it
			// makes a private key for us
			kp, err = keypairs.Create(p.computeClient, keypairs.CreateOpts{Name: resources.ResourceName}).Extract()
			if err != nil {
				return
			}

			resources.PrivateKey = kp.PrivateKey

			// err = ioutil.WriteFile("./"+resources.ResourceName+".pem", []byte(kp.PrivateKey), 0600) // this is the file needed to ssh in manually, using ssh -i wr-deployment.pem ubuntu@xxx.xx.xx.xxx *** we need a way of programatically finding or setting the name of the login user...
			// if err != nil {
			//     return
			// }
			// err = ioutil.WriteFile("./"+resources.ResourceName+".key", []byte(kp.PublicKey), 0600)
			// if err != nil {
			//     return
			// }
		} else {
			return
		}
	}
	resources.Details["keypair"] = kp.Name

	// based on hostname, see if we're currently running on an openstack server,
	// in which case we'll use this server's security group and network.
	hostname, err := os.Hostname()
	inCloud := false
	if err == nil {
		pager := servers.List(p.computeClient, servers.ListOpts{})
		err = pager.EachPage(func(page pagination.Page) (bool, error) {
			serverList, err := servers.ExtractServers(page)
			if err != nil {
				return false, err
			}

			for _, server := range serverList {
				if server.Name == hostname {
					p.ownName = hostname

					// get the first networkUUID we come across *** not sure
					// what the other possibilities are and what else we can do
					// instead
					for networkName := range server.Addresses {
						networkUUID, _ := networks.IDFromName(p.networkClient, networkName)
						if networkUUID != "" {
							p.networkName = networkName
							p.networkUUID = networkUUID
							break
						}
					}

					// get the first security group *** again, not sure how to
					// pick the "best" if more than one
					for _, smap := range server.SecurityGroups {
						if value, found := smap["name"]; found && value.(string) != "" {
							p.securityGroup = value.(string)
							break
						}
					}

					if p.networkUUID != "" && p.securityGroup != "" {
						inCloud = true
						return false, nil
					}
				}
			}

			return true, nil
		})
	}

	//*** actually, if in cloud, we should create a security group that allows
	// the given ports, only accessible by things in the current security group
	// don't create any more resources if we're already running in OpenStack
	if inCloud {
		return
	}

	// get/create security group
	pager := secgroups.List(p.computeClient)
	var group *secgroups.SecurityGroup
	foundGroup := false
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		groupList, err := secgroups.ExtractSecurityGroups(page)
		if err != nil {
			return false, err
		}

		for _, g := range groupList {
			if g.Name == resources.ResourceName {
				group = &g
				foundGroup = true
				return false, nil
			}
		}

		return true, nil
	})
	if err != nil {
		return
	}
	if !foundGroup {
		// create a new security group with rules allowing the desired ports
		group, err = secgroups.Create(p.computeClient, secgroups.CreateOpts{Name: resources.ResourceName, Description: "access amongst wr-spawned nodes"}).Extract()
		if err != nil {
			return
		}

		//*** check if the rules are already there, in case we previously died
		// between previous line and this one
		for _, port := range requiredPorts {
			_, err = secgroups.CreateRule(p.computeClient, secgroups.CreateRuleOpts{
				ParentGroupID: group.ID,
				FromPort:      port,
				ToPort:        port,
				IPProtocol:    "TCP",
				CIDR:          "0.0.0.0/0", // FromGroupID: group.ID if we were creating a head node and then wanted a rule for all worker nodes...
			}).Extract()
			if err != nil {
				return
			}
		}

		// ICMP may help networking work as expected
		_, err = secgroups.CreateRule(p.computeClient, secgroups.CreateRuleOpts{
			ParentGroupID: group.ID,
			FromPort:      0,
			ToPort:        0, // *** results in a port of '0', which is not the same as "ALL ICMP" which then says "Any" in the web interface
			IPProtocol:    "ICMP",
			CIDR:          "0.0.0.0/0",
		}).Extract()
		if err != nil {
			return
		}
	}
	resources.Details["secgroup"] = group.ID
	p.securityGroup = resources.ResourceName

	// get/create network
	var network *networks.Network
	networkID, err := networks.IDFromName(p.networkClient, resources.ResourceName)
	if err != nil {
		if _, notfound := err.(gophercloud.ErrResourceNotFound); notfound {
			// create a network for ourselves
			network, err = networks.Create(p.networkClient, networks.CreateOpts{Name: resources.ResourceName, AdminStateUp: gophercloud.Enabled}).Extract()
			if err != nil {
				return
			}
			networkID = network.ID
		} else {
			return
		}
	} else {
		network, err = networks.Get(p.networkClient, networkID).Extract()
		if err != nil {
			return
		}
	}
	resources.Details["network"] = networkID
	p.networkName = resources.ResourceName
	p.networkUUID = networkID

	// get/create subnet
	var subnetID string
	if len(network.Subnets) == 1 {
		subnetID = network.Subnets[0]
		// *** check it's valid? could we end up with more than 1 subnet?
	} else {
		// add a big enough subnet
		var gip = new(string)
		*gip = "192.168.0.1"
		var subnet *subnets.Subnet
		subnet, err = subnets.Create(p.networkClient, subnets.CreateOpts{
			NetworkID:      networkID,
			CIDR:           "192.168.0.0/16",
			GatewayIP:      gip,
			DNSNameservers: dnsNameServers[:], // this is critical, or servers on new networks can't be ssh'd to for many minutes
			IPVersion:      4,
			Name:           resources.ResourceName,
		}).Extract()
		if err != nil {
			return
		}
		subnetID = subnet.ID
	}
	resources.Details["subnet"] = subnetID

	// get/create router
	var routerID string
	pager = routers.List(p.networkClient, routers.ListOpts{Name: resources.ResourceName})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		routerList, err := routers.ExtractRouters(page)
		if err != nil {
			return false, err
		}
		routerID = routerList[0].ID
		// *** check it's valid? could we end up with more than 1 router?
		return false, nil
	})
	if err != nil {
		return
	}
	if routerID == "" {
		var router *routers.Router
		router, err = routers.Create(p.networkClient, routers.CreateOpts{
			Name:         resources.ResourceName,
			GatewayInfo:  &routers.GatewayInfo{NetworkID: p.externalNetworkID},
			AdminStateUp: gophercloud.Enabled,
		}).Extract()
		if err != nil {
			return
		}

		routerID = router.ID

		// add our subnet
		_, err = routers.AddInterface(p.networkClient, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract()
		if err != nil {
			// if this fails, we'd be stuck with a useless router, so we try and
			// delete it
			routers.Delete(p.networkClient, router.ID)
			return
		}
	}
	resources.Details["router"] = routerID

	return
}

// flavors returns all our flavors.
func (p *openstackp) flavors() map[string]Flavor {
	return p.fmap
}

// getQuota achieves the aims of GetQuota().
func (p *openstackp) getQuota() (quota *Quota, err error) {
	// query our quota
	q, err := quotasets.Get(p.computeClient, os.Getenv("OS_TENANT_ID")).Extract()
	if err != nil {
		return
	}
	quota = &Quota{
		MaxRAM:       q.Ram,
		MaxCores:     q.Cores,
		MaxInstances: q.Instances,
	}

	// query all servers to figure out what we've used of our quota
	// (*** gophercloud currently doesn't implement getting this properly)
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			quota.UsedInstances++
			f, found := p.fmap[server.Flavor["id"].(string)]
			if found { // should always be found...
				quota.UsedCores += f.Cores
				quota.UsedRAM += f.RAM
			}
		}

		return true, nil
	})

	return
}

// spawn achieves the aims of Spawn()
func (p *openstackp) spawn(resources *Resources, osPrefix string, flavorID string, externalIP bool) (serverID string, serverIP string, adminPass string, err error) {
	// get available images, pick the one that matches desired OS
	// *** rackspace API lets you filter on eg. os_distro=ubuntu and os_version=12.04; can we do the same here?
	pager := images.ListDetail(p.computeClient, images.ListOpts{Status: "ACTIVE"})
	var imageID string
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		imageList, err := images.ExtractImages(page)
		if err != nil {
			return false, err
		}

		for _, i := range imageList {
			if i.Progress == 100 && strings.HasPrefix(i.Name, osPrefix) {
				imageID = i.ID
				return false, nil
			}
		}

		return true, nil
	})
	if err != nil {
		return
	}

	// create the server with a unique name
	server, err := servers.Create(p.computeClient, keypairs.CreateOptsExt{
		CreateOptsBuilder: servers.CreateOpts{
			Name:           uniqueResourceName(resources.ResourceName),
			FlavorRef:      flavorID,
			ImageRef:       imageID,
			SecurityGroups: []string{p.securityGroup},
			Networks:       []servers.Network{{UUID: p.networkUUID}},
			// UserData []byte (will be base64-encoded for me)
			// Metadata map[string]string
		},
		KeyName: resources.ResourceName,
	}).Extract()
	if err != nil {
		return
	}

	// wait for it to come up; servers.WaitForStatus has a timeout, but it
	// doesn't always work, so we roll our own
	waitForActive := make(chan error)
	go func() {
		timeout := time.After(60 * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-ticker.C:
				current, err := servers.Get(p.computeClient, server.ID).Extract()
				if err != nil {
					ticker.Stop()
					waitForActive <- err
					return
				}
				if current.Status == "ACTIVE" {
					ticker.Stop()
					waitForActive <- nil
					return
				}
				if current.Status == "ERROR" {
					ticker.Stop()
					waitForActive <- errors.New("there was an error in bringing up the new server")
					return
				}
				continue
			case <-timeout:
				ticker.Stop()
				waitForActive <- errors.New("timed out waiting for server to become ACTIVE")
				return
			}
		}
	}()
	err = <-waitForActive
	if err != nil {
		// since we're going to return an error that we failed to spawn, try and
		// delete the bad server in case it is still there
		servers.Delete(p.computeClient, server.ID)
		return
	}
	// *** NB. it can still take some number of seconds before I can ssh to it

	serverID = server.ID
	adminPass = server.AdminPass

	// get the servers IP; if we error for any reason we'll delete the server
	// first, because without an IP it's useless

	if externalIP {
		// give it a floating ip
		var floatingIP string
		floatingIP, err = p.getAvailableFloatingIP()
		if err != nil {
			p.destroyServer(serverID)
			return
		}

		// associate floating ip with server *** we have a race condition
		// between finding/creating free floating IP above, and using it here
		err = floatingips.AssociateInstance(p.computeClient, serverID, floatingips.AssociateOpts{
			FloatingIP: floatingIP,
		}).ExtractErr()
		if err != nil {
			p.destroyServer(serverID)
			return
		}

		serverIP = floatingIP
	} else {
		// find its auto-assigned internal ip *** there must be a better way of
		// doing this...
		allNetworkAddressPages, serr := servers.ListAddressesByNetwork(p.computeClient, serverID, p.networkName).AllPages()
		if serr != nil {
			p.destroyServer(serverID)
			err = serr
			return
		}
		allNetworkAddresses, serr := servers.ExtractNetworkAddresses(allNetworkAddressPages)
		if serr != nil {
			p.destroyServer(serverID)
			err = serr
			return
		}
		for _, address := range allNetworkAddresses {
			if address.Version == 4 && strings.HasPrefix(address.Address, "192.168") {
				serverIP = address.Address
				break
			}
		}
	}

	return
}

// checkServer achieves the aims of CheckServer()
func (p *openstackp) checkServer(serverID string) (bool, error) {
	server, err := servers.Get(p.computeClient, serverID).Extract()
	if err != nil {
		if err.Error() == "Resource not found" {
			return false, nil
		}
		return false, err
	}

	return server.Status == "ACTIVE", nil
}

// destroyServer achieves the aims of DestroyServer()
func (p *openstackp) destroyServer(serverID string) (err error) {
	err = servers.Delete(p.computeClient, serverID).ExtractErr()
	if err != nil {
		return
	}

	// wait for it to really be deleted, or we won't be able to
	// delete the router and network later; the following returns
	// an error of "Resource not found" as soon as the server
	// is not there anymore; we don't care about any others
	servers.WaitForStatus(p.computeClient, serverID, "xxxx", 60)
	return
}

// tearDown achieves the aims of TearDown()
func (p *openstackp) tearDown(resources *Resources) (err error) {
	// throughout we'll ignore errors because we want to try and delete
	// as much as possible; we'll end up returning the last error we encountered

	// delete servers, except for ourselves
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				p.destroyServer(server.ID) // ignore errors, just try to delete others
			}
		}

		return true, nil
	})

	if p.ownName == "" {
		// delete router
		if id := resources.Details["router"]; id != "" {
			if subnetid := resources.Details["subnet"]; subnetid != "" {
				// remove the interface from our router first
				_, err = routers.RemoveInterface(p.networkClient, id, routers.RemoveInterfaceOpts{SubnetID: subnetid}).Extract()
			}
			err = routers.Delete(p.networkClient, id).ExtractErr()
		}

		// delete network (and its subnet)
		if id := resources.Details["network"]; id != "" {
			err = networks.Delete(p.networkClient, id).ExtractErr()
		}

		// delete secgroup
		if id := resources.Details["secgroup"]; id != "" {
			err = secgroups.Delete(p.computeClient, id).ExtractErr()
		}
	}

	// delete keypair, unless we're running in OpenStack and securityGroup and
	// keypair have the same resourcename, indicating our current server needs
	// the same keypair we used to spawn our servers
	if id := resources.Details["keypair"]; id != "" {
		if p.ownName == "" || (p.securityGroup != "" && p.securityGroup != id) {
			err = keypairs.Delete(p.computeClient, id).ExtractErr()
		}
	}

	return
}

// getAvailableFloatingIP gets or creates an unused floating ip
func (p *openstackp) getAvailableFloatingIP() (floatingIP string, err error) {
	// find any existing floating ips
	allFloatingIPPages, err := floatingips.List(p.computeClient).AllPages()
	if err != nil {
		return
	}

	allFloatingIPs, err := floatingips.ExtractFloatingIPs(allFloatingIPPages)
	if err != nil {
		return
	}

	for _, fIP := range allFloatingIPs {
		if fIP.InstanceID == "" {
			floatingIP = fIP.IP
			break
		}
	}
	if floatingIP == "" {
		// create a new one
		fIP, ferr := floatingips.Create(p.computeClient, floatingips.CreateOpts{
			Pool: p.poolName,
		}).Extract()
		if ferr != nil {
			err = ferr
			return
		}
		floatingIP = fIP.IP
		// *** should we delete these during TearDown? fIP.Delete(p.computeClient, fIP.ID) ...
	}

	return
}
