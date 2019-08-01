// Copyright © 2016-2018 Genome Research Limited
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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VividCortex/ewma"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/bootfromvolume"
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
	"github.com/hashicorp/go-multierror"
	"github.com/inconshreveable/log15"
	"github.com/jpillora/backoff"
	"golang.org/x/crypto/ssh"
)

// initialServerSpawnTimeout is how long we wait for the first server we ever
// spawn to go from 'BUILD' state to something else; hopefully it is OK for this
// to be very large, since if there's an actual problem bringing up a server it
// should return an error or go to a different state, at which point we no
// longer consider the timeout. This is only used for the initial wait time;
// subsequently we learn how long recent builds actually take.
const initialServerSpawnTimeout = 20 * time.Minute

// invalidFlavorIDMsg is used to report when a certain flavor ID does not exist
const invalidFlavorIDMsg = "invalid flavor ID"

// openstack only allows certain chars in resource names, so we have a regexp to
// check.
var openstackValidResourceNameRegexp = regexp.MustCompile(`^[\w -]+$`)

// openstackEnvs contains the environment variable names we need to connect to
// OpenStack. These are only the required ones for all intalls; other env vars
// are required but it varies which ones. Gophercloud also considers:
// OS_USERID, OS_TENANT_ID, OS_TENANT_NAME, OS_DOMAIN_ID, OS_DOMAIN_NAME,
// OS_PROJECT_ID, OS_PROJECT_NAME (with *PROJECT* overriding *TENANT*, and only
// one of the *DOMAIN* variables being allowed to be set). We also use
// OS_POOL_NAME to determine the name of the network to get floating IPs from.
var openstackReqEnvs = [...]string{"OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_REGION_NAME"}
var openstackMaybeEnvs = [...]string{"OS_USERID", "OS_TENANT_ID", "OS_TENANT_NAME", "OS_DOMAIN_ID", "OS_PROJECT_DOMAIN_ID", "OS_DOMAIN_NAME", "OS_USER_DOMAIN_NAME", "OS_PROJECT_ID", "OS_PROJECT_NAME", "OS_POOL_NAME"}

// openstackp is our implementer of provideri
type openstackp struct {
	lastFlavorCache   time.Time
	externalNetworkID string
	networkName       string
	networkUUID       string
	ownName           string
	poolName          string
	securityGroup     string
	spawnTimes        ewma.MovingAverage
	spawnTimesVolume  ewma.MovingAverage
	tenantID          string
	log15.Logger
	computeClient   *gophercloud.ServiceClient
	errorBackoff    *backoff.Backoff
	fmap            map[string]*Flavor
	imap            map[string]*images.Image
	ipNet           *net.IPNet
	networkClient   *gophercloud.ServiceClient
	ownServer       *servers.Server
	fmapMutex       sync.RWMutex
	imapMutex       sync.RWMutex
	createdKeyPair  bool
	useConfigDrive  bool
	hasDefaultGroup bool
	spawnFailed     bool
}

// requiredEnv returns envs that are definitely required.
func (p *openstackp) requiredEnv() []string {
	return openstackReqEnvs[:]
}

// maybeEnv returns envs that might be required.
func (p *openstackp) maybeEnv() []string {
	return openstackMaybeEnvs[:]
}

// initialize uses our required environment variables to authenticate with
// OpenStack and create some clients we will use in the other methods.
func (p *openstackp) initialize(logger log15.Logger) error {
	p.Logger = logger.New("cloud", "openstack")

	// gophercloud uses non-standard env var names, so convert if necessary
	if os.Getenv("OS_DOMAIN_ID") == "" && os.Getenv("OS_PROJECT_DOMAIN_ID") != "" {
		err := os.Setenv("OS_DOMAIN_ID", os.Getenv("OS_PROJECT_DOMAIN_ID"))
		if err != nil {
			return err
		}
	}

	if os.Getenv("OS_DOMAIN_ID") == "" && os.Getenv("OS_DOMAIN_NAME") == "" && os.Getenv("OS_USER_DOMAIN_NAME") != "" {
		err := os.Setenv("OS_DOMAIN_NAME", os.Getenv("OS_USER_DOMAIN_NAME"))
		if err != nil {
			return err
		}
	}

	// we use a non-standard env var to find the default network from which to
	// get floating IPs from, which defaults depending on age of OpenStack
	// installation
	p.poolName = os.Getenv("OS_POOL_NAME")
	if p.poolName == "" {
		if os.Getenv("OS_TENANT_ID") != "" {
			p.poolName = "nova"
		} else {
			p.poolName = "public"
		}
	}

	// authenticate
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return err
	}
	if opts.TenantID == "" {
		return fmt.Errorf("either OS_TENANT_ID or OS_PROJECT_ID must be set")
	}
	p.tenantID = opts.TenantID
	opts.AllowReauth = true
	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return err
	}

	// make a compute client
	p.computeClient, err = openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return err
	}

	// make a network client
	p.networkClient, err = openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{
		//Name:   "neutron", //*** "services can have the same Type but a different Name, which is why [...] Name [is] sometimes needed... but how do I see the available names?
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		return err
	}

	// flavors and images are retrieved on-demand via caching methods that store
	// in these maps
	p.fmap = make(map[string]*Flavor)
	p.imap = make(map[string]*images.Image)

	// to get a reasonable new server timeout we'll keep track of how long it
	// takes to spawn them using an exponentially weighted moving average. We
	// keep track of servers spawned with and without volumes separately, since
	// volume creation takes much longer.
	p.spawnTimes = ewma.NewMovingAverage()
	p.spawnTimesVolume = ewma.NewMovingAverage()

	// spawn() backs off on new requests if the previous one failed, tracked
	// with a Backoff
	p.errorBackoff = &backoff.Backoff{
		Min:    1 * time.Second,
		Max:    initialServerSpawnTimeout,
		Factor: 3,
		Jitter: true,
	}

	return err
}

// cacheFlavors retrieves the current list of flavors from OpenStack and caches
// them in p. Old no-longer existent flavors are kept forever, so we can still
// see what resources old instances are using.
func (p *openstackp) cacheFlavors() error {
	p.fmapMutex.Lock()
	defer func() {
		p.lastFlavorCache = time.Now()
		p.fmapMutex.Unlock()
	}()

	pager := flavors.ListDetail(p.computeClient, flavors.ListOpts{})
	return pager.EachPage(func(page pagination.Page) (bool, error) {
		flavorList, err := flavors.ExtractFlavors(page)
		if err != nil {
			return false, err
		}

		for _, f := range flavorList {
			p.fmap[f.ID] = &Flavor{
				ID:    f.ID,
				Name:  f.Name,
				Cores: f.VCPUs,
				RAM:   f.RAM,
				Disk:  f.Disk,
			}
		}
		return true, nil
	})
}

// getFlavor retrieves the desired flavor by id from the cache. If it's not in
// the cache, will call cacheFlavors() to get any newly added flavors. If still
// not in the cache, returns nil and an error.
func (p *openstackp) getFlavor(flavorID string) (*Flavor, error) {
	p.fmapMutex.RLock()
	flavor, found := p.fmap[flavorID]
	p.fmapMutex.RUnlock()
	if !found {
		err := p.cacheFlavors()
		if err != nil {
			return nil, err
		}

		p.fmapMutex.RLock()
		flavor, found = p.fmap[flavorID]
		p.fmapMutex.RUnlock()
		if !found {
			return nil, errors.New(invalidFlavorIDMsg + ": " + flavorID)
		}
	}
	return flavor, nil
}

// cacheImages retrieves the current list of images from OpenStack and caches
// them in p. Old no-longer existent images are kept forever, so we can still
// see what images old instances are using.
func (p *openstackp) cacheImages() error {
	p.imapMutex.Lock()
	defer p.imapMutex.Unlock()
	pager := images.ListDetail(p.computeClient, images.ListOpts{Status: "ACTIVE"})
	return pager.EachPage(func(page pagination.Page) (bool, error) {
		imageList, errf := images.ExtractImages(page)
		if errf != nil {
			return false, errf
		}

		for _, i := range imageList {
			if i.Progress == 100 {
				thisI := i // copy before storing ref
				p.imap[i.ID] = &thisI
				p.imap[i.Name] = &thisI
			}
		}

		return true, nil
	})
}

// getImage retrieves the desired image by name or id prefix from the cache. If
// it's not in the cache, will call cacheImages() to get any newly added images.
// If still not in the cache, returns nil and an error.
func (p *openstackp) getImage(prefix string) (*images.Image, error) {
	image := p.getImageFromCache(prefix)
	if image != nil {
		return image, nil
	}

	err := p.cacheImages()
	if err != nil {
		return nil, err
	}

	image = p.getImageFromCache(prefix)
	if image != nil {
		return image, nil
	}

	return nil, errors.New("no OS image with prefix [" + prefix + "] was found")
}

// getImageFromCache is used by getImage(); don't call this directly.
func (p *openstackp) getImageFromCache(prefix string) *images.Image {
	p.imapMutex.RLock()
	defer p.imapMutex.RUnlock()

	// find an exact match
	if i, found := p.imap[prefix]; found {
		return i
	}

	// failing that, find a random prefix match
	for _, i := range p.imap {
		if strings.HasPrefix(i.Name, prefix) || strings.HasPrefix(i.ID, prefix) {
			return i
		}
	}
	return nil
}

// deploy achieves the aims of Deploy().
func (p *openstackp) deploy(resources *Resources, requiredPorts []int, useConfigDrive bool, gatewayIP, cidr string, dnsNameServers []string) error {
	// the resource name can only contain letters, numbers, underscores,
	// spaces and hyphens
	if !openstackValidResourceNameRegexp.MatchString(resources.ResourceName) {
		return Error{"openstack", "deploy", ErrBadResourceName}
	}

	// spawn() needs to figure out which of a server's ips are local, so we
	// parse and store the CIDR
	var err error
	_, p.ipNet, err = net.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	p.useConfigDrive = useConfigDrive

	// get/create key pair
	kp, err := keypairs.Get(p.computeClient, resources.ResourceName).Extract()
	if err != nil {
		if _, notfound := err.(gophercloud.ErrDefault404); notfound {
			// create a new keypair; we can't just let Openstack create one for
			// us because in latest versions it does not return a DER encoded
			// key, which is what GO built-in library supports.
			privateKey, errk := rsa.GenerateKey(rand.Reader, 2048)
			if errk != nil {
				return errk
			}
			privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
			privateKeyPEMBytes := pem.EncodeToMemory(privateKeyPEM)
			pub, errk := ssh.NewPublicKey(&privateKey.PublicKey)
			if errk != nil {
				return errk
			}
			publicKeyStr := ssh.MarshalAuthorizedKey(pub)

			kp, err = keypairs.Create(p.computeClient, keypairs.CreateOpts{Name: resources.ResourceName, PublicKey: string(publicKeyStr)}).Extract()
			if err != nil {
				return err
			}
			p.createdKeyPair = true

			resources.PrivateKey = string(privateKeyPEMBytes)
			// NB: reliant on err now being nil here, hence errk above, since we
			// don't want to make err local to this block
		} else {
			return err
		}
	}
	resources.Details["keypair"] = kp.Name

	if len(requiredPorts) > 0 {
		// get/create security group, and see if there's a default group
		pager := secgroups.List(p.computeClient)
		var group *secgroups.SecurityGroup
		defaultGroupExists := false
		foundGroup := false
		err = pager.EachPage(func(page pagination.Page) (bool, error) {
			groupList, errf := secgroups.ExtractSecurityGroups(page)
			if errf != nil {
				return false, errf
			}

			for _, g := range groupList {
				if g.Name == resources.ResourceName {
					g := g // pin
					group = &g
					foundGroup = true
					if defaultGroupExists {
						return false, nil
					}
				}
				if g.Name == "default" {
					defaultGroupExists = true
					if foundGroup {
						return false, nil
					}
				}
			}

			return true, nil
		})
		if err != nil {
			return err
		}
		if !foundGroup {
			// create a new security group with rules allowing the desired ports
			group, err = secgroups.Create(p.computeClient, secgroups.CreateOpts{Name: resources.ResourceName, Description: "access amongst wr-spawned nodes"}).Extract()
			if err != nil {
				return err
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
					return err
				}
			}

			// ICMP may help networking work as expected
			_, err = secgroups.CreateRule(p.computeClient, secgroups.CreateRuleOpts{
				ParentGroupID: group.ID,
				FromPort:      -1,
				ToPort:        -1, // -1 results in "Any", the same as "ALL ICMP" in Horizon
				IPProtocol:    "ICMP",
				CIDR:          "0.0.0.0/0",
			}).Extract()
			if err != nil {
				return err
			}
		}
		resources.Details["secgroup"] = group.ID
		p.securityGroup = resources.ResourceName
		p.hasDefaultGroup = defaultGroupExists
	}

	// don't create any more resources if we're already running in OpenStack
	if p.inCloud() {
		// work out our network uuid, needed for spawning later
	NETWORKS:
		for networkName := range p.ownServer.Addresses {
			networkUUID, erri := networks.IDFromName(p.networkClient, networkName)
			if erri != nil {
				return erri
			}
			if networkUUID != "" {
				network, errg := networks.Get(p.networkClient, networkUUID).Extract()
				if errg != nil {
					return errg
				}
				for _, subnetID := range network.Subnets {
					subnet, errg := subnets.Get(p.networkClient, subnetID).Extract()
					if errg != nil {
						return errg
					}
					if subnet.CIDR == cidr {
						p.networkName = networkName
						p.networkUUID = networkUUID
						break NETWORKS
					}
				}
			}
		}

		if p.networkUUID == "" {
			return Error{"openstack", "deploy", ErrBadCIDR}
		}
		return nil
	}

	// get/create network
	var network *networks.Network
	networkID, err := networks.IDFromName(p.networkClient, resources.ResourceName)
	if err != nil {
		if _, notfound := err.(gophercloud.ErrResourceNotFound); notfound {
			// create a network for ourselves
			network, err = networks.Create(p.networkClient, networks.CreateOpts{Name: resources.ResourceName, AdminStateUp: gophercloud.Enabled}).Extract()
			if err != nil {
				return err
			}
			networkID = network.ID
		} else {
			return err
		}
	} else {
		network, err = networks.Get(p.networkClient, networkID).Extract()
		if err != nil {
			return err
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
		*gip = gatewayIP
		var subnet *subnets.Subnet
		subnet, err = subnets.Create(p.networkClient, subnets.CreateOpts{
			NetworkID:      networkID,
			CIDR:           cidr,
			GatewayIP:      gip,
			DNSNameservers: dnsNameServers, // this is critical, or servers on new networks can't be ssh'd to for many minutes
			IPVersion:      4,
			Name:           resources.ResourceName,
		}).Extract()
		if err != nil {
			return err
		}
		subnetID = subnet.ID
	}
	resources.Details["subnet"] = subnetID

	// get/create router
	var routerID string
	pager := routers.List(p.networkClient, routers.ListOpts{Name: resources.ResourceName})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		routerList, errf := routers.ExtractRouters(page)
		if errf != nil {
			return false, errf
		}
		routerID = routerList[0].ID
		// *** check it's valid? could we end up with more than 1 router?
		return false, nil
	})
	if err != nil {
		return err
	}
	if routerID == "" {
		// get the external network id
		if p.externalNetworkID == "" {
			p.externalNetworkID, err = networks.IDFromName(p.networkClient, p.poolName)
			if err != nil {
				return err
			}
		}

		var router *routers.Router
		router, err = routers.Create(p.networkClient, routers.CreateOpts{
			Name:         resources.ResourceName,
			GatewayInfo:  &routers.GatewayInfo{NetworkID: p.externalNetworkID},
			AdminStateUp: gophercloud.Enabled,
		}).Extract()
		if err != nil {
			return err
		}

		routerID = router.ID

		// add our subnet
		_, err = routers.AddInterface(p.networkClient, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract()
		if err != nil {
			// if this fails, we'd be stuck with a useless router, so we try and
			// delete it
			routers.Delete(p.networkClient, router.ID)
			return err
		}
	}
	resources.Details["router"] = routerID

	return err
}

// getCurrentServers returns details of other servers with the given resource
// name prefix.
func (p *openstackp) getCurrentServers(resources *Resources) ([][]string, error) {
	var sdetails [][]string
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				serverIP, errg := p.getServerIP(server.ID)
				if errg != nil {
					continue
				}

				details := []string{server.ID, serverIP, server.Name, server.AdminPass}
				sdetails = append(sdetails, details)
			}
		}

		return true, nil
	})
	return sdetails, err
}

// inCloud checks if we're currently running on an OpenStack server based on our
// hostname matching a host in OpenStack.
func (p *openstackp) inCloud() bool {
	hostname, err := os.Hostname()
	inCloud := false
	if err == nil {
		pager := servers.List(p.computeClient, servers.ListOpts{})
		err = pager.EachPage(func(page pagination.Page) (bool, error) {
			serverList, errf := servers.ExtractServers(page)
			if errf != nil {
				return false, errf
			}

			for _, server := range serverList {
				if nameToHostName(server.Name) == hostname {
					p.ownName = hostname
					server := server // pin (not needed since we return, but just to be careful)
					p.ownServer = &server
					inCloud = true
					return false, nil
				}
			}

			return true, nil
		})

		if err != nil {
			p.Warn("paging through servers failed", "err", err)
		}
	}

	return inCloud
}

// flavors returns all our flavors.
func (p *openstackp) flavors() map[string]*Flavor {
	// update the cached flavors at most once every half hour
	p.fmapMutex.RLock()
	if time.Since(p.lastFlavorCache) > 30*time.Minute {
		p.fmapMutex.RUnlock()
		err := p.cacheFlavors()
		if err != nil {
			p.Warn("failed to cache available flavors", "err", err)
		}
		p.fmapMutex.RLock()
	}
	fmap := make(map[string]*Flavor)
	for key, val := range p.fmap {
		fmap[key] = val
	}
	p.fmapMutex.RUnlock()
	return fmap
}

// getQuota achieves the aims of GetQuota().
func (p *openstackp) getQuota() (*Quota, error) {
	// query our quota
	q, err := quotasets.Get(p.computeClient, p.tenantID).Extract()
	if err != nil {
		return nil, err
	}
	quota := &Quota{
		MaxRAM:       q.RAM,
		MaxCores:     q.Cores,
		MaxInstances: q.Instances,
		// MaxVolume:    q.Volume, //*** https://github.com/gophercloud/gophercloud/issues/234#issuecomment-273666521 : no support for getting volume quotas...
	}

	// query all servers to figure out what we've used of our quota
	// (*** gophercloud currently doesn't implement getting this properly)
	err = p.cacheFlavors()
	if err != nil {
		p.Warn("failed to cache available flavors", "err", err)
	}
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, errf := servers.ExtractServers(page)
		if errf != nil {
			return false, errf
		}

		for _, server := range serverList {
			quota.UsedInstances++
			f, errf := p.getFlavor(server.Flavor["id"].(string))
			// since we're going through all servers, not just ones we created
			// ourselves, it's possible that there is an old server with a
			// flavor that no longer exists, so we allow invalid flavor errors
			if errf != nil {
				if strings.HasPrefix(errf.Error(), invalidFlavorIDMsg) {
					p.Warn("an old server has a flavor that no longer exists; our remaining quota estimation will be off", "server", server.ID, "flavor", server.Flavor["id"].(string))
				} else {
					return false, errf
				}
			}
			if f != nil {
				quota.UsedCores += f.Cores
				quota.UsedRAM += f.RAM
			}
			//*** how to find out how much volume storage this is using?...
		}

		return true, nil
	})

	return quota, err
}

// spawn achieves the aims of Spawn()
func (p *openstackp) spawn(resources *Resources, osPrefix string, flavorID string, diskGB int, externalIP bool, usingQuotaCh chan bool) (serverID, serverIP, serverName, adminPass string, err error) {
	// get the image that matches desired OS
	image, err := p.getImage(osPrefix)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	flavor, err := p.getFlavor(flavorID)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	// if the OS image itself specifies a minimum disk size and it's higher than
	// requested disk, increase our requested disk
	if image.MinDisk > diskGB {
		diskGB = image.MinDisk
	}

	// if we previously had a problem spawning a server, wait before attempting
	// again
	if p.spawnFailed {
		time.Sleep(p.errorBackoff.Duration())
	}

	// we'll use the security group we created, and the "default" one if it
	// exists
	var secGroups []string
	if p.securityGroup != "" {
		secGroups = append(secGroups, p.securityGroup)
		if p.hasDefaultGroup {
			secGroups = append(secGroups, "default")
		}
	}

	// create the server with a unique name
	var server *servers.Server
	serverName = uniqueResourceName(resources.ResourceName)
	createOpts := servers.CreateOpts{
		Name:           serverName,
		FlavorRef:      flavorID,
		ImageRef:       image.ID,
		SecurityGroups: secGroups,
		Networks:       []servers.Network{{UUID: p.networkUUID}},
		ConfigDrive:    &p.useConfigDrive,
		UserData:       sentinelInitScript,
	}
	var createdVolume bool
	if diskGB > flavor.Disk {
		server, err = bootfromvolume.Create(p.computeClient, keypairs.CreateOptsExt{
			CreateOptsBuilder: bootfromvolume.CreateOptsExt{
				CreateOptsBuilder: createOpts,
				BlockDevice: []bootfromvolume.BlockDevice{
					{
						UUID:                image.ID,
						SourceType:          bootfromvolume.SourceImage,
						DeleteOnTermination: true,
						DestinationType:     bootfromvolume.DestinationVolume,
						VolumeSize:          diskGB,
					},
				},
			},
			KeyName: resources.ResourceName,
		}).Extract()
		createdVolume = true
	} else {
		server, err = servers.Create(p.computeClient, keypairs.CreateOptsExt{
			CreateOptsBuilder: createOpts,
			KeyName:           resources.ResourceName,
		}).Extract()
	}

	usingQuotaCh <- true

	if err != nil {
		p.spawnFailed = true
		return serverID, serverIP, serverName, adminPass, err
	}

	// wait for it to come up; servers.WaitForStatus has a timeout, but it
	// doesn't always work, so we roll our own
	waitForActive := make(chan error)
	go func() {
		defer internal.LogPanic(p.Logger, "spawn", false)

		var timeoutS float64
		if createdVolume {
			timeoutS = p.spawnTimesVolume.Value() * 4
		} else {
			timeoutS = p.spawnTimes.Value() * 4
		}
		if timeoutS <= 0 {
			timeoutS = initialServerSpawnTimeout.Seconds()
		}
		if timeoutS < 90 {
			timeoutS = 90
		}
		timeout := time.After(time.Duration(timeoutS) * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				current, errf := servers.Get(p.computeClient, server.ID).Extract()
				if errf != nil {
					ticker.Stop()
					waitForActive <- errf
					return
				}
				if current.Status == "ACTIVE" {
					ticker.Stop()
					spawnSecs := time.Since(start).Seconds()
					if createdVolume {
						p.spawnTimesVolume.Add(spawnSecs)
					} else {
						p.spawnTimes.Add(spawnSecs)
					}
					waitForActive <- nil
					return
				}
				if current.Status == "ERROR" {
					ticker.Stop()
					msg := current.Fault.Message
					if msg == "" {
						msg = "the server is in ERROR state following an unknown problem"
					}
					waitForActive <- errors.New(msg)
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
		p.spawnFailed = true
		delerr := servers.Delete(p.computeClient, server.ID).ExtractErr()
		if delerr != nil {
			err = fmt.Errorf("%s\nadditionally, there was an error deleting the bad server: %s", err, delerr)
		}
		return serverID, serverIP, serverName, adminPass, err
	}
	if p.spawnFailed {
		p.errorBackoff.Reset()
	}
	p.spawnFailed = false

	// *** NB. it can still take some number of seconds before I can ssh to it

	serverID = server.ID
	adminPass = server.AdminPass

	// get the servers IP; if we error for any reason we'll delete the server
	// first, because without an IP it's useless
	if externalIP {
		// give it a floating ip
		floatingIP, errf := p.getAvailableFloatingIP()
		if errf != nil {
			errd := p.destroyServer(serverID)
			if errd != nil {
				p.Warn("server destruction after no IP failed", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errf
		}

		// associate floating ip with server *** we have a race condition
		// between finding/creating free floating IP above, and using it here
		errf = floatingips.AssociateInstance(p.computeClient, serverID, floatingips.AssociateOpts{
			FloatingIP: floatingIP,
		}).ExtractErr()
		if errf != nil {
			errd := p.destroyServer(serverID)
			if errd != nil {
				p.Warn("server destruction after not associating IP failed", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errf
		}

		serverIP = floatingIP
	} else {
		var errg error
		serverIP, errg = p.getServerIP(serverID)
		if errg != nil {
			errd := p.destroyServer(serverID)
			if errd != nil {
				p.Warn("server destruction after not finding ip", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errg
		}
	}

	return serverID, serverIP, serverName, adminPass, err
}

// errIsNoHardware returns true if error contains "There are not enough hosts
// available".
func (p *openstackp) errIsNoHardware(err error) bool {
	return strings.Contains(err.Error(), "There are not enough hosts available")
}

// getServerIP tries to find the auto-assigned internal ip address of the server
// with the given ID.
func (p *openstackp) getServerIP(serverID string) (string, error) {
	// *** there must be a better way of doing this...
	allNetworkAddressPages, err := servers.ListAddressesByNetwork(p.computeClient, serverID, p.networkName).AllPages()
	if err != nil {
		return "", err
	}
	allNetworkAddresses, err := servers.ExtractNetworkAddresses(allNetworkAddressPages)
	if err != nil {
		return "", err
	}
	for _, address := range allNetworkAddresses {
		if address.Version == 4 {
			ip := net.ParseIP(address.Address)
			if ip != nil {
				if p.ipNet.Contains(ip) {
					return address.Address, nil
				}
			}
		}
	}
	return "", nil
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
func (p *openstackp) destroyServer(serverID string) error {
	err := servers.Delete(p.computeClient, serverID).ExtractErr()
	if err != nil {
		return err
	}

	// wait for it to really be deleted, or we won't be able to
	// delete the router and network later; the following returns
	// an error of "Resource not found" as soon as the server
	// is not there anymore; we don't care about any others
	errs := servers.WaitForStatus(p.computeClient, serverID, "xxxx", 60)
	if errs.Error() != "Resource not found" {
		p.Warn("server destruction failed", "server", serverID, "err", errs)
	}
	return err
}

// tearDown achieves the aims of TearDown()
func (p *openstackp) tearDown(resources *Resources) error {
	// throughout we'll ignore errors because we want to try and delete
	// as much as possible; we'll end up returning a concatenation of all of
	// them though
	var merr *multierror.Error

	// delete servers, except for ourselves
	t := time.Now()
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				t = time.Now()
				errd := p.destroyServer(server.ID)
				p.Debug("delete server", "time", time.Since(t), "id", server.ID)
				if errd != nil {
					// ignore errors, just try to delete others
					p.Warn("server destruction during teardown failed", "server", server.ID, "err", errd)
				}
			}
		}

		return true, nil
	})
	merr = p.combineError(merr, err)

	if p.ownName == "" {
		// delete router
		if id := resources.Details["router"]; id != "" {
			if subnetid := resources.Details["subnet"]; subnetid != "" {
				// remove the interface from our router first, retrying for a
				// few seconds on failure, since destroyed servers may not have
				// fully terminated yet
				tries := 0
				for {
					t = time.Now()
					_, errr := routers.RemoveInterface(p.networkClient, id, routers.RemoveInterfaceOpts{SubnetID: subnetid}).Extract()
					p.Debug("remove router interface", "time", time.Since(t), "routerid", id, "subnetid", subnetid, "err", errr)
					if errr != nil {
						tries++
						if tries >= 10 {
							merr = p.combineError(merr, errr)
							break
						}
						<-time.After(1 * time.Second)
						continue
					}
					break
				}
			}
			t = time.Now()
			err := routers.Delete(p.networkClient, id).ExtractErr()
			p.Debug("delete router", "time", time.Since(t), "id", id, "err", err)
			merr = p.combineError(merr, err)
		}

		// delete network (and its subnet)
		if id := resources.Details["network"]; id != "" {
			t = time.Now()
			err := networks.Delete(p.networkClient, id).ExtractErr()
			p.Debug("delete network (auto-deletes subnet)", "time", time.Since(t), "id", id, "err", err)
			merr = p.combineError(merr, err)
		}

		// delete secgroup
		if id := resources.Details["secgroup"]; id != "" {
			t = time.Now()
			err := secgroups.Delete(p.computeClient, id).ExtractErr()
			p.Debug("delete security group", "time", time.Since(t), "id", id, "err", err)
			merr = p.combineError(merr, err)
		}
	}

	// delete keypair, unless we're running in OpenStack and securityGroup and
	// keypair have the same resourcename, indicating our current server needs
	// the same keypair we used to spawn our servers. Bypass the exception if
	// we definitely created the key pair this session
	if id := resources.Details["keypair"]; id != "" {
		if p.createdKeyPair || p.ownName == "" || (p.securityGroup != "" && p.securityGroup != id) {
			t = time.Now()
			err := keypairs.Delete(p.computeClient, id).ExtractErr()
			p.Debug("delete keypair", "time", time.Since(t), "id", id, "err", err)
			merr = p.combineError(merr, err)
			resources.PrivateKey = ""
		}
	}

	return merr.ErrorOrNil()
}

// combineError Append()s the given err on merr, but ignores err if it is
// "Resource not found".
func (p *openstackp) combineError(merr *multierror.Error, err error) *multierror.Error {
	if err != nil && !strings.Contains(err.Error(), "Resource not found") {
		merr = multierror.Append(merr, err)
	}
	return merr
}

// getAvailableFloatingIP gets or creates an unused floating ip
func (p *openstackp) getAvailableFloatingIP() (string, error) {
	// find any existing floating ips
	allFloatingIPPages, err := floatingips.List(p.computeClient).AllPages()
	if err != nil {
		return "", err
	}

	allFloatingIPs, err := floatingips.ExtractFloatingIPs(allFloatingIPPages)
	if err != nil {
		return "", err
	}

	var floatingIP string
	for _, fIP := range allFloatingIPs {
		if fIP.InstanceID == "" {
			floatingIP = fIP.IP
			break
		}
	}
	if floatingIP == "" {
		// create a new one
		fIP, err := floatingips.Create(p.computeClient, floatingips.CreateOpts{
			Pool: p.poolName,
		}).Extract()
		if err != nil {
			return "", err
		}
		floatingIP = fIP.IP
		// *** should we delete these during TearDown? fIP.Delete(p.computeClient, fIP.ID) ...
	}

	return floatingIP, nil
}
