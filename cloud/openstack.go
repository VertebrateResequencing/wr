/*******************************************************************************
 * Copyright (c) 2016-2021, 2023-2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: David K Jackson <david.jackson@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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

// This file contains a provideri implementation for OpenStack

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VividCortex/ewma"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/secgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	imageimages "github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	networkfloatingips "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/hashicorp/go-multierror"
	"github.com/jpillora/backoff"
	"github.com/sb10/waitgroup"
	"golang.org/x/crypto/ssh"
)

// initialServerSpawnTimeout is how long we wait for the first server we ever
// spawn to go from 'BUILD' state to something else; hopefully it is OK for this
// to be very large, since if there's an actual problem bringing up a server it
// should return an error or go to a different state, at which point we no
// longer consider the timeout. This is only used for the initial wait time;
// subsequently we learn how long recent builds actually take.
const initialServerSpawnTimeout = 20 * time.Minute

// maxServerErrorBackoff is the most time we will wait before trying to create
// another server, following a series of creation failures.
const maxServerErrorBackoff = 1 * time.Minute

// destroyServerTimeout is how long we wait for server destruction requests to
// be successful before giving up.
const destroyServerTimeout = 2 * time.Minute

// destroyServersTimeout is how long we wait for multiple server destructions
// running in parallel to return.
const destroyServersTimeout = 2 * destroyServerTimeout

// destroyServerCheckFrequency is frequently server status is checked after a
// destroy request until the server is gone.
const destroyServerCheckFrequency = 250 * time.Millisecond

// minimumServerSpawnTimeoutSecs is the minimum amount of time we wait for
// servers to change from 'BUILD' state. It can be longer than this based on
// learning.
const minimumServerSpawnTimeoutSecs = 180

const ipVersion4 = 4

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
var (
	openstackReqEnvs   = [...]string{"OS_AUTH_URL", "OS_USERNAME", "OS_PASSWORD", "OS_REGION_NAME"}
	openstackMaybeEnvs = [...]string{"OS_USERID", "OS_TENANT_ID", "OS_TENANT_NAME", "OS_DOMAIN_ID", "OS_PROJECT_DOMAIN_ID", "OS_DOMAIN_NAME", "OS_USER_DOMAIN_NAME", "OS_PROJECT_ID", "OS_PROJECT_NAME", "OS_POOL_NAME"}
)

var (
	errInvalidFlavorID       = errors.New(invalidFlavorIDMsg)
	errInvalidServerFlavorID = errors.New("server flavor id is not a string")
)

// openstackp is our implementer of provideri
type openstackp struct {
	lastFlavorCache   time.Time
	externalNetworkID string
	networkName       string
	ownName           string
	poolName          string
	securityGroup     string
	spawnTimes        ewma.MovingAverage
	spawnTimesVolume  ewma.MovingAverage
	tenantID          string
	computeClient     *gophercloud.ServiceClient
	errorBackoff      *backoff.Backoff
	fmap              map[string]*Flavor
	imap              map[string]*imageimages.Image
	imageClient       *gophercloud.ServiceClient
	ipNet             *net.IPNet
	networkClient     *gophercloud.ServiceClient
	ownServer         *servers.Server
	fmapMutex         sync.RWMutex
	imapMutex         sync.RWMutex
	stMutex           sync.RWMutex
	spMutex           sync.RWMutex
	createdKeyPair    bool
	useConfigDrive    bool
	hasDefaultGroup   bool
	spawnFailed       bool
	networks          []servers.Network
	createdPorts      map[string][]string
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
func (p *openstackp) initialize() error {
	// we use a non-standard env var to find the default network from which to
	// get floating IPs from, which defaults depending on age of OpenStack
	// installation
	// *** A Nova "pool" can be thought of as a Neutron public subnet. It should
	// be possible to query/search for a subnet using the Neutron API without
	// having to provide a project ID and pool name.
	p.poolName = os.Getenv("OS_POOL_NAME")
	if p.poolName == "" {
		if os.Getenv("OS_TENANT_ID") != "" {
			p.poolName = "nova"
		} else {
			p.poolName = "public"
		}
	}

	// authenticate
	ctx := context.Background()

	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return err
	}

	opts.AllowReauth = true

	provider, err := openstack.AuthenticatedClient(ctx, opts)
	if err != nil {
		return err
	}

	endpoint := gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	}

	// make a compute client
	p.computeClient, err = openstack.NewComputeV2(provider, endpoint)
	if err != nil {
		return err
	}

	if opts.TenantID == "" {
		identityClient, erri := openstack.NewIdentityV3(provider, endpoint)
		if erri != nil {
			return err
		}

		project, erri := tokens.Create(ctx, identityClient, &opts).ExtractProject()
		if erri != nil {
			return err
		}

		if project.ID == "" {
			return fmt.Errorf("either OS_TENANT_ID or OS_PROJECT_ID must be set")
		}

		p.tenantID = project.ID
	} else {
		p.tenantID = opts.TenantID
	}

	// make a network client
	p.networkClient, err = openstack.NewNetworkV2(provider, endpoint)
	if err != nil {
		return err
	}

	p.imageClient, err = openstack.NewImageV2(provider, endpoint)
	if err != nil {
		return err
	}

	// flavors and images are retrieved on-demand via caching methods that store
	// in these maps
	p.fmap = make(map[string]*Flavor)
	p.imap = make(map[string]*imageimages.Image)

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
		Max:    maxServerErrorBackoff,
		Factor: 1.5,
		Jitter: true,
	}

	p.createdPorts = make(map[string][]string)

	return err
}

// cacheFlavors retrieves the current list of flavors from OpenStack and caches
// them in p. Old no-longer existent flavors are kept forever, so we can still
// see what resources old instances are using.
func (p *openstackp) cacheFlavors(ctx context.Context) error {
	p.fmapMutex.Lock()
	defer func() {
		p.lastFlavorCache = time.Now()
		p.fmapMutex.Unlock()
	}()

	pager := flavors.ListDetail(p.computeClient, flavors.ListOpts{})

	return pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
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
func (p *openstackp) getFlavor(ctx context.Context, flavorID string) (*Flavor, error) {
	p.fmapMutex.RLock()
	flavor, found := p.fmap[flavorID]
	p.fmapMutex.RUnlock()
	if !found {
		err := p.cacheFlavors(ctx)
		if err != nil {
			return nil, err
		}

		p.fmapMutex.RLock()
		flavor, found = p.fmap[flavorID]
		p.fmapMutex.RUnlock()
		if !found {
			return nil, fmt.Errorf("%w: %s", errInvalidFlavorID, flavorID)
		}
	}
	return flavor, nil
}

// cacheImages retrieves the current list of images from OpenStack and caches
// them in p. Old no-longer existent images are kept forever, so we can still
// see what images old instances are using.
func (p *openstackp) cacheImages(ctx context.Context) error {
	p.imapMutex.Lock()
	defer p.imapMutex.Unlock()

	pager := imageimages.List(p.imageClient, imageimages.ListOpts{Status: imageimages.ImageStatusActive})

	return pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		imageList, errf := imageimages.ExtractImages(page)
		if errf != nil {
			return false, errf
		}

		for _, i := range imageList {
			thisI := i // copy before storing ref
			p.imap[i.ID] = &thisI
			p.imap[i.Name] = &thisI
		}

		return true, nil
	})
}

// getImage retrieves the desired image by name or id prefix from the cache. If
// it's not in the cache, will call cacheImages() to get any newly added images.
// If still not in the cache, returns nil and an error.
func (p *openstackp) getImage(ctx context.Context, prefix string) (*imageimages.Image, error) {
	image := p.getImageFromCache(prefix)
	if image != nil {
		return image, nil
	}

	err := p.cacheImages(ctx)
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
func (p *openstackp) getImageFromCache(prefix string) *imageimages.Image {
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

func enabledBool() *bool {
	enabled := true

	return &enabled
}

func (p *openstackp) networkIDFromName(ctx context.Context, name string) (string, error) {
	pages, err := networks.List(p.networkClient, networks.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return "", err
	}

	all, err := networks.ExtractNetworks(pages)
	if err != nil {
		return "", err
	}

	ids := make([]string, len(all))
	for i := range all {
		ids[i] = all[i].ID
	}

	switch count := len(ids); count {
	case 0:
		return "", gophercloud.ErrResourceNotFound{Name: name, ResourceType: "network"}
	case 1:
		return ids[0], nil
	default:
		return "", gophercloud.ErrMultipleResourcesFound{Name: name, Count: count, ResourceType: "network"}
	}
}

func (p *openstackp) getServerPortID(ctx context.Context, serverID string) (string, error) {
	pages, err := ports.List(p.networkClient, ports.ListOpts{
		DeviceID:  serverID,
		NetworkID: p.networks[0].UUID,
	}).AllPages(ctx)
	if err != nil {
		return "", err
	}

	allPorts, err := ports.ExtractPorts(pages)
	if err != nil {
		return "", err
	}

	for _, port := range allPorts {
		for _, fixedIP := range port.FixedIPs {
			ip := net.ParseIP(fixedIP.IPAddress)
			if ip != nil && p.ipNet.Contains(ip) {
				return port.ID, nil
			}
		}
	}

	return "", gophercloud.ErrResourceNotFound{Name: serverID, ResourceType: "server port"}
}

// deploy achieves the aims of Deploy().
func (p *openstackp) deploy(ctx context.Context, resources *Resources, requiredPorts []int, useConfigDrive bool, gatewayIP, cidr string, dnsNameServers []string) error {
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
	kp, err := keypairs.Get(ctx, p.computeClient, resources.ResourceName, nil).Extract()
	if err != nil {
		if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
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

			createOpts := keypairs.CreateOpts{
				Name:      resources.ResourceName,
				PublicKey: string(publicKeyStr),
			}

			kp, err = keypairs.Create(ctx, p.computeClient, createOpts).Extract()
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

		err = pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
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
			createOpts := secgroups.CreateOpts{
				Name:        resources.ResourceName,
				Description: "access amongst wr-spawned nodes",
			}

			group, err = secgroups.Create(ctx, p.computeClient, createOpts).Extract()
			if err != nil {
				return err
			}

			//*** check if the rules are already there, in case we previously died
			// between previous line and this one
			for _, port := range requiredPorts {
				_, err = secgroups.CreateRule(ctx, p.computeClient, secgroups.CreateRuleOpts{
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
			_, err = secgroups.CreateRule(ctx, p.computeClient, secgroups.CreateRuleOpts{
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
	var mainNetworkUUID string
	var otherNetworkUUIDs []string
	if p.inCloud(ctx) {
		// work out our network uuid, needed for spawning later
		for networkName := range p.ownServer.Addresses {
			networkUUID, erri := p.networkIDFromName(ctx, networkName)
			if erri != nil {
				return erri
			}

			if networkUUID != "" {
				network, errg := networks.Get(ctx, p.networkClient, networkUUID).Extract()
				if errg != nil {
					return errg
				}
				for _, subnetID := range network.Subnets {
					subnet, errg := subnets.Get(ctx, p.networkClient, subnetID).Extract()
					if errg != nil {
						return errg
					}
					if subnet.CIDR == cidr {
						p.networkName = networkName
						mainNetworkUUID = networkUUID

						break
					}
				}

				if networkUUID != mainNetworkUUID {
					otherNetworkUUIDs = append(otherNetworkUUIDs, networkUUID)
				}
			}
		}

		if mainNetworkUUID == "" {
			return Error{"openstack", "deploy", ErrBadCIDR}
		}

		p.networks = append(p.networks, servers.Network{UUID: mainNetworkUUID})
		for _, uuid := range otherNetworkUUIDs {
			p.networks = append(p.networks, servers.Network{UUID: uuid})
		}

		return nil
	}

	// get/create network
	var network *networks.Network

	networkID, err := p.networkIDFromName(ctx, resources.ResourceName)
	if err != nil {
		var notFound gophercloud.ErrResourceNotFound
		if errors.As(err, &notFound) {
			// create a network for ourselves
			createOpts := networks.CreateOpts{
				Name:         resources.ResourceName,
				AdminStateUp: enabledBool(),
			}

			network, err = networks.Create(ctx, p.networkClient, createOpts).Extract()
			if err != nil {
				return err
			}
			networkID = network.ID
		} else {
			return err
		}
	} else {
		network, err = networks.Get(ctx, p.networkClient, networkID).Extract()
		if err != nil {
			return err
		}
	}
	resources.Details["network"] = networkID
	p.networkName = resources.ResourceName
	p.networks = append(p.networks, servers.Network{UUID: networkID})

	// get/create subnet
	var subnetID string
	if len(network.Subnets) == 1 {
		subnetID = network.Subnets[0]
		// *** check it's valid? could we end up with more than 1 subnet?
	} else {
		// add a big enough subnet
		gip := new(string)
		*gip = gatewayIP
		var subnet *subnets.Subnet

		subnet, err = subnets.Create(ctx, p.networkClient, subnets.CreateOpts{
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

	err = pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
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
			p.externalNetworkID, err = p.networkIDFromName(ctx, p.poolName)
			if err != nil {
				return err
			}
		}

		var router *routers.Router

		router, err = routers.Create(ctx, p.networkClient, routers.CreateOpts{
			Name:         resources.ResourceName,
			GatewayInfo:  &routers.GatewayInfo{NetworkID: p.externalNetworkID},
			AdminStateUp: enabledBool(),
		}).Extract()
		if err != nil {
			return err
		}

		routerID = router.ID

		// add our subnet
		_, err = routers.AddInterface(ctx, p.networkClient, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract()
		if err != nil {
			// if this fails, we'd be stuck with a useless router, so we try and
			// delete it
			routers.Delete(ctx, p.networkClient, router.ID)
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
	err := pager.EachPage(context.Background(), func(ctx context.Context, page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				serverIP, errg := p.getServerIP(ctx, server.ID)
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
func (p *openstackp) inCloud(ctx context.Context) bool {
	hostname, err := os.Hostname()
	inCloud := false
	if err == nil {
		pager := servers.List(p.computeClient, servers.ListOpts{})

		err = pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
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
			clog.Warn(ctx, "paging through servers failed", "err", err)
		}
	}

	return inCloud
}

// flavors returns all our flavors.
func (p *openstackp) flavors(ctx context.Context) map[string]*Flavor {
	// update the cached flavors at most once every half hour
	p.fmapMutex.RLock()
	if time.Since(p.lastFlavorCache) > 30*time.Minute {
		p.fmapMutex.RUnlock()

		err := p.cacheFlavors(ctx)
		if err != nil {
			clog.Warn(ctx, "failed to cache available flavors", "err", err)
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
func (p *openstackp) getQuota(ctx context.Context) (*Quota, error) {
	// query our quota
	q, err := quotasets.Get(ctx, p.computeClient, p.tenantID).Extract()
	if err != nil {
		return nil, err
	}
	quota := &Quota{
		MaxRAM:       q.RAM,
		MaxCores:     q.Cores,
		MaxInstances: q.Instances,
		// MaxVolume: q.Volume,
		// *** https://github.com/gophercloud/gophercloud/v2/issues/234#issuecomment-273666521 :
		// no support for getting volume quotas...
	}

	// query all servers to figure out what we've used of our quota
	// (*** gophercloud currently doesn't implement getting this properly)
	err = p.cacheFlavors(ctx)
	if err != nil {
		clog.Warn(ctx, "failed to cache available flavors", "err", err)
	}
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err = pager.EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
		serverList, errf := servers.ExtractServers(page)
		if errf != nil {
			return false, errf
		}

		for _, server := range serverList {
			quota.UsedInstances++

			flavorID, ok := server.Flavor["id"].(string)
			if !ok {
				return false, fmt.Errorf("%w: %s", errInvalidServerFlavorID, server.ID)
			}

			f, errf := p.getFlavor(ctx, flavorID)
			// since we're going through all servers, not just ones we created
			// ourselves, it's possible that there is an old server with a
			// flavor that no longer exists, so we allow invalid flavor errors
			if errf != nil {
				if errors.Is(errf, errInvalidFlavorID) {
					warnStr := "an old server has a flavor that no longer exists; our remaining quota estimation will be off"
					clog.Warn(ctx, warnStr, "server", server.ID, "flavor", flavorID)
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
func (p *openstackp) spawn(ctx context.Context, resources *Resources, osPrefix string, flavorID string, diskGB int, externalIP bool, usingQuotaCh chan bool) (serverID, serverIP, serverName, adminPass string, err error) {
	// get the image that matches desired OS
	image, err := p.getImage(ctx, osPrefix)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	flavor, err := p.getFlavor(ctx, flavorID)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	// if the OS image itself specifies a minimum disk size and it's higher than
	// requested disk, increase our requested disk
	if image.MinDiskGigabytes > diskGB {
		diskGB = image.MinDiskGigabytes
	}

	// if we previously had a problem spawning a server, wait before attempting
	// again
	p.spMutex.RLock()
	sf := p.spawnFailed
	p.spMutex.RUnlock()
	if sf {
		wait := p.errorBackoff.Duration()
		clog.Warn(ctx, "server spawn waiting due to prior failures", "wait", wait)
		time.Sleep(wait)
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
		Networks:       []servers.Network{p.networks[0]},
		ConfigDrive:    &p.useConfigDrive,
		UserData:       sentinelInitScript,
	}
	var createdVolume bool
	t := time.Now()
	if diskGB > flavor.Disk {
		createOpts.BlockDevice = []servers.BlockDevice{
			{
				UUID:                image.ID,
				SourceType:          servers.SourceImage,
				DeleteOnTermination: true,
				DestinationType:     servers.DestinationVolume,
				VolumeSize:          diskGB,
			},
		}
		server, err = servers.Create(ctx, p.computeClient, keypairs.CreateOptsExt{
			CreateOptsBuilder: createOpts,
			KeyName:           resources.ResourceName,
		}, nil).Extract()
		createdVolume = true
	} else {
		server, err = servers.Create(ctx, p.computeClient, keypairs.CreateOptsExt{
			CreateOptsBuilder: createOpts,
			KeyName:           resources.ResourceName,
		}, nil).Extract()
	}

	if server != nil {
		serverID = server.ID
	}

	clog.Debug(ctx, "server create attempted", "took", time.Since(t), "id", serverID, "worked", err == nil)

	usingQuotaCh <- true

	if err != nil {
		p.spMutex.Lock()
		p.spawnFailed = true
		p.spMutex.Unlock()
		return serverID, serverIP, serverName, adminPass, err
	}

	// wait for it to come up; servers.WaitForStatus has a timeout, but it
	// doesn't always work, so we roll our own
	waitForActive := make(chan error)
	go func() {
		defer internal.LogPanic(ctx, "spawn", false)

		var timeoutS float64
		var typical int
		p.stMutex.RLock()
		if createdVolume {
			timeoutS = p.spawnTimesVolume.Value() * 4
			typical = int(p.spawnTimesVolume.Value())
		} else {
			timeoutS = p.spawnTimes.Value() * 4
			typical = int(p.spawnTimes.Value())
		}
		p.stMutex.RUnlock()
		if timeoutS <= 0 {
			timeoutS = initialServerSpawnTimeout.Seconds()
		}
		if timeoutS < minimumServerSpawnTimeoutSecs {
			timeoutS = minimumServerSpawnTimeoutSecs
		}
		timeout := time.After(time.Duration(timeoutS) * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		start := time.Now()
		attempts := 0
		for {
			select {
			case <-ticker.C:
				current, errf := servers.Get(ctx, p.computeClient, serverID).Extract()
				attempts++
				if errf != nil {
					ticker.Stop()
					waitForActive <- errf
					return
				}
				if current.Status == "ACTIVE" {
					ticker.Stop()
					clog.Debug(ctx, "server became ACTIVE", "id", serverID, "took", time.Since(start), "polls", attempts)
					spawnSecs := time.Since(start).Seconds()
					p.stMutex.Lock()
					if createdVolume {
						p.spawnTimesVolume.Add(spawnSecs)
					} else {
						p.spawnTimes.Add(spawnSecs)
					}
					p.stMutex.Unlock()
					waitForActive <- nil
					return
				}
				if current.Status == "ERROR" {
					ticker.Stop()
					msg := current.Fault.Message
					if msg == "" {
						msg = "unknown problem"
					}
					waitForActive <- fmt.Errorf("server %s is in ERROR state after %s and %d polls: %s", serverID, time.Since(start), attempts, msg)
					return
				}
				continue
			case <-timeout:
				ticker.Stop()

				current, errf := servers.Get(ctx, p.computeClient, serverID).Extract()
				status := "unknown"
				if errf == nil {
					status = current.Status
				}
				waitForActive <- fmt.Errorf("server %s is %s after %ds, timing out on it ever becoming ACTIVE (typical time to becoming active has been %ds)", server.ID, status, int(time.Since(start).Seconds()), typical)
				return
			}
		}
	}()
	err = <-waitForActive
	if err != nil {
		// since we're going to return an error that we failed to spawn, try and
		// delete the bad server in case it is still there
		p.spMutex.Lock()
		p.spawnFailed = true
		p.spMutex.Unlock()

		delerr := servers.Delete(ctx, p.computeClient, server.ID).ExtractErr()
		if delerr != nil {
			err = fmt.Errorf("%w\nadditionally, there was an error deleting the bad server: %w", err, delerr)
		}
		return serverID, serverIP, serverName, adminPass, err
	}
	p.spMutex.Lock()
	if p.spawnFailed {
		p.errorBackoff.Reset()
	}
	p.spawnFailed = false
	p.spMutex.Unlock()

	// *** NB. it can still take some number of seconds before I can ssh to it

	adminPass = server.AdminPass

	// get the servers IP; if we error for any reason we'll delete the server
	// first, because without an IP it's useless
	if externalIP {
		// give it a floating ip
		floatingIP, errf := p.getAvailableFloatingIP(ctx)
		if errf != nil {
			errd := p.destroyServer(ctx, serverID)
			if errd != nil {
				clog.Warn(ctx, "server destruction after no IP failed", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errf
		}

		// associate floating ip with server *** we have a race condition
		// between finding/creating free floating IP above, and using it here
		portID, errp := p.getServerPortID(ctx, serverID)
		if errp != nil {
			errd := p.destroyServer(ctx, serverID)
			if errd != nil {
				clog.Warn(ctx, "server destruction after not finding port failed", "server", serverID, "err", errd)
			}

			return serverID, serverIP, serverName, adminPass, errp
		}

		_, errf = networkfloatingips.Update(ctx, p.networkClient, floatingIP.ID, networkfloatingips.UpdateOpts{
			PortID: &portID,
		}).Extract()
		if errf != nil {
			errd := p.destroyServer(ctx, serverID)
			if errd != nil {
				clog.Warn(ctx, "server destruction after not associating IP failed", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errf
		}

		serverIP = floatingIP.FloatingIP
	} else {
		var errg error

		serverIP, errg = p.getServerIP(ctx, serverID)
		if errg != nil {
			errd := p.destroyServer(ctx, serverID)
			if errd != nil {
				clog.Warn(ctx, "server destruction after not finding ip", "server", serverID, "err", errd)
			}
			return serverID, serverIP, serverName, adminPass, errg
		}
	}

	// if we have multiple networks, add ports for the others
	if len(p.networks) > 1 {
		for i, network := range p.networks {
			if i == 0 {
				continue
			}

			portCreateOtps := ports.CreateOpts{
				AdminStateUp: enabledBool(),
				NetworkID:    network.UUID,
				Name:         fmt.Sprintf("%s-%s-%d", resources.ResourceName, serverID, i),
			}

			port, errC := ports.Create(ctx, p.networkClient, portCreateOtps).Extract()
			if errC != nil {
				clog.Warn(ctx, "failed to create port", "err", errC, "network", network.UUID)

				continue
			}
			p.createdPorts[serverID] = append(p.createdPorts[serverID], port.ID)

			attachOpts := attachinterfaces.CreateOpts{
				PortID: port.ID,
			}

			_, errC = attachinterfaces.Create(ctx, p.computeClient, serverID, attachOpts).Extract()
			if errC != nil {
				clog.Warn(ctx, "failed to attach port", "err", errC, "network", network.UUID, "port", port.ID, "server", serverID)

				continue
			} else {
				clog.Debug(ctx, "attached port for extra network", "server", serverID, "network", network.UUID, "port", port.ID)
			}
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
func (p *openstackp) getServerIP(ctx context.Context, serverID string) (string, error) {
	// *** there must be a better way of doing this...
	allNetworkAddressPages, err := servers.ListAddressesByNetwork(p.computeClient, serverID, p.networkName).AllPages(ctx)
	if err != nil {
		return "", err
	}
	allNetworkAddresses, err := servers.ExtractNetworkAddresses(allNetworkAddressPages)
	if err != nil {
		return "", err
	}
	for _, address := range allNetworkAddresses {
		if address.Version != ipVersion4 {
			continue
		}

		ip := net.ParseIP(address.Address)
		if ip != nil && p.ipNet.Contains(ip) {
			return address.Address, nil
		}
	}
	return "", nil
}

// checkServer achieves the aims of CheckServer().
func (p *openstackp) checkServer(serverID string) (bool, error) {
	server, err := servers.Get(context.Background(), p.computeClient, serverID).Extract()
	if err != nil {
		if errorIsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return server.Status == "ACTIVE", nil
}

func errorIsNotFound(err error) bool {
	var notFound gophercloud.ErrResourceNotFound

	return errors.As(err, &notFound) ||
		gophercloud.ResponseCodeIs(err, http.StatusNotFound) ||
		strings.Contains(err.Error(), "Resource not found")
}

// checkServer achieves the aims of ServerIsKnown().
func (p *openstackp) serverIsKnown(serverID string) (bool, error) {
	server, err := servers.Get(context.Background(), p.computeClient, serverID).Extract()
	if err != nil {
		if errorIsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return server != nil, nil
}

// destroyServer achieves the aims of DestroyServer().
func (p *openstackp) destroyServer(ctx context.Context, serverID string) error {
	err := servers.Delete(ctx, p.computeClient, serverID).ExtractErr()
	if err != nil {
		if errorIsNotFound(err) {
			return nil
		}
		return err
	}

	// wait for it to really be deleted, or we won't be able to
	// delete the router and network later; rather that use
	// servers.WaitForStatus which could force us to wait on the timeout, we
	// just wait up to 2mins to get a Resource not found error
	limit := time.After(destroyServerTimeout)
	ticker := time.NewTicker(destroyServerCheckFrequency)
	var server *servers.Server
WAIT:
	for {
		select {
		case <-ticker.C:
			// servers.Get() call can get stuck for a long time, so let that
			// time out as well
			serverCh := make(chan *servers.Server, 1)
			getErrCh := make(chan error, 1)
			go func() {
				s, e := servers.Get(ctx, p.computeClient, serverID).Extract()
				serverCh <- s
				getErrCh <- e
			}()
			select {
			case server = <-serverCh:
				err = <-getErrCh
				if err != nil {
					ticker.Stop()
					break WAIT
				}
			case <-limit:
				ticker.Stop()
				err = fmt.Errorf("server not deleted? timed out getting its status")
				break WAIT
			}
		case <-limit:
			ticker.Stop()
			break WAIT
		}
	}
	if err == nil {
		err = fmt.Errorf("server not deleted, still has status '%s'", server.Status)
	}

	if errorIsNotFound(err) {
		err = nil
	}

	// and delete any ports we made for this server
	if createdPorts, created := p.createdPorts[serverID]; created {
		for _, uuid := range createdPorts {
			errP := ports.Delete(ctx, p.networkClient, uuid).ExtractErr()
			if errP != nil {
				clog.Warn(ctx, "failed to delete a port", "id", uuid, "server", serverID)
			}
		}
		delete(p.createdPorts, serverID)
	}

	return err
}

// tearDown achieves the aims of TearDown()
func (p *openstackp) tearDown(ctx context.Context, resources *Resources) error {
	// throughout we'll ignore errors because we want to try and delete
	// as much as possible; we'll end up returning a concatenation of all of
	// them though
	var merr *multierror.Error

	// delete servers, except for ourselves
	var toDestroy []string
	pager := servers.List(p.computeClient, servers.ListOpts{})
	err := pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				toDestroy = append(toDestroy, server.ID)
			}
		}

		return true, nil
	})
	merr = p.combineError(merr, err)

	var didSomething bool
	if len(toDestroy) > 0 {
		didSomething = true
		wg := waitgroup.New()
		wgk := wg.Add(len(toDestroy))
		for _, sid := range toDestroy {
			go func(id string) {
				defer internal.LogPanic(ctx, "cloud openstack tearDown destroyServer", false)
				defer wg.Done(wgk)

				t := time.Now()
				errd := p.destroyServer(ctx, id)
				clog.Debug(ctx, "delete server", "time", time.Since(t), "id", id)
				if errd != nil {
					// ignore errors, just try to delete others
					clog.Warn(ctx, "server destruction during teardown failed", "server", id, "err", errd)
				}
			}(sid)
		}
		wg.Wait(destroyServersTimeout)
	}

	if p.ownName == "" {
		// delete router
		if id := resources.Details["router"]; id != "" {
			if subnetid := resources.Details["subnet"]; subnetid != "" {
				// remove the interface from our router first, retrying for a
				// few seconds on failure, since destroyed servers may not have
				// fully terminated yet
				tries := 0
				for {
					t := time.Now()
					removeOpts := routers.RemoveInterfaceOpts{
						SubnetID: subnetid,
					}
					_, errr := routers.RemoveInterface(ctx, p.networkClient, id, removeOpts).Extract()
					clog.Debug(ctx, "remove router interface", "time", time.Since(t), "routerid",
						id, "subnetid", subnetid, "err", errr)
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
			t := time.Now()
			err := routers.Delete(ctx, p.networkClient, id).ExtractErr()
			clog.Debug(ctx, "delete router", "time", time.Since(t), "id", id, "err", err)
			if err == nil {
				didSomething = true
			}
			merr = p.combineError(merr, err)
		}

		// delete network (and its subnet)
		if id := resources.Details["network"]; id != "" {
			t := time.Now()
			err := networks.Delete(ctx, p.networkClient, id).ExtractErr()
			clog.Debug(ctx, "delete network (auto-deletes subnet)", "time", time.Since(t), "id", id, "err", err)
			if err == nil {
				didSomething = true
			}
			merr = p.combineError(merr, err)
		}

		// delete secgroup
		if id := resources.Details["secgroup"]; id != "" {
			t := time.Now()
			err := secgroups.Delete(ctx, p.computeClient, id).ExtractErr()
			clog.Debug(ctx, "delete security group", "time", time.Since(t), "id", id, "err", err)
			if err == nil {
				didSomething = true
			}
			merr = p.combineError(merr, err)
		}
	}

	// delete keypair, unless we're running in OpenStack and securityGroup and
	// keypair have the same resourcename, indicating our current server needs
	// the same keypair we used to spawn our servers. Bypass the exception if
	// we definitely created the key pair this session
	if id := resources.Details["keypair"]; id != "" {
		if p.createdKeyPair || p.ownName == "" || (p.securityGroup != "" && p.securityGroup != id) {
			t := time.Now()
			err := keypairs.Delete(ctx, p.computeClient, id, nil).ExtractErr()
			clog.Debug(ctx, "delete keypair", "time", time.Since(t), "id", id, "err", err)
			// keypairs are not credential-specific enough, so we don't consider
			// deleting one as didSomething
			merr = p.combineError(merr, err)
			resources.PrivateKey = ""
		}
	}

	rerr := merr.ErrorOrNil()
	if rerr == nil && !didSomething {
		return Error{"openstack", "tearDown", ErrNoTearDown}
	}

	return rerr
}

// combineError Append()s the given err on merr, but ignores err if it is
// "Resource not found".
func (p *openstackp) combineError(merr *multierror.Error, err error) *multierror.Error {
	if err != nil && !errorIsNotFound(err) {
		merr = multierror.Append(merr, err)
	}

	return merr
}

// getAvailableFloatingIP gets or creates an unused floating ip
func (p *openstackp) getAvailableFloatingIP(ctx context.Context) (*networkfloatingips.FloatingIP, error) {
	// find any existing floating ips
	allFloatingIPPages, err := networkfloatingips.List(p.networkClient, networkfloatingips.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}

	allFloatingIPs, err := networkfloatingips.ExtractFloatingIPs(allFloatingIPPages)
	if err != nil {
		return nil, err
	}

	for _, fIP := range allFloatingIPs {
		if fIP.PortID == "" {
			thisFIP := fIP

			return &thisFIP, nil
		}
	}

	return p.createFloatingIP(ctx)
}

func (p *openstackp) createFloatingIP(ctx context.Context) (*networkfloatingips.FloatingIP, error) {
	if p.externalNetworkID == "" {
		networkID, err := p.networkIDFromName(ctx, p.poolName)
		if err != nil {
			return nil, err
		}

		p.externalNetworkID = networkID
	}

	createOpts := networkfloatingips.CreateOpts{
		FloatingNetworkID: p.externalNetworkID,
	}
	// *** should we delete these during TearDown? fIP.Delete(p.computeClient, fIP.ID) ...
	return networkfloatingips.Create(ctx, p.networkClient, createOpts).Extract()
}
