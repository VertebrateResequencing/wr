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
	"maps"
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

// serverErrorBackoffFactor is the multiplier applied to the spawn() retry
// backoff after each consecutive server creation failure.
const serverErrorBackoffFactor = 1.5

// keyBits is the size of the RSA key we generate for ssh access to servers.
const keyBits = 2048

// spawnTimeoutMultiplier multiplies the recent average spawn time to derive the
// timeout we allow for a new server to become ACTIVE.
const spawnTimeoutMultiplier = 4

// maxRouterInterfaceRemovalTries is how many times we retry removing a router
// interface during tearDown before giving up.
const maxRouterInterfaceRemovalTries = 10

// invalidFlavorIDMsg is used to report when a certain flavor ID does not exist.
const invalidFlavorIDMsg = "invalid flavor ID"

// openstack only allows certain chars in resource names, so we have a regexp to
// check.
var openstackValidResourceNameRegexp = regexp.MustCompile(`^[\w -]+$`)

// environment variable names used to connect to OpenStack. They are named
// constants because they are referenced both here and in tests.
const (
	envOSAuthURL                     = "OS_AUTH_URL"
	envOSUsername                    = "OS_USERNAME"
	envOSPassword                    = "OS_PASSWORD"
	envOSRegionName                  = "OS_REGION_NAME"
	envOSUserID                      = "OS_USERID"
	envOSUserIDAlt                   = "OS_USER_ID"
	envOSTenantID                    = "OS_TENANT_ID"
	envOSTenantName                  = "OS_TENANT_NAME"
	envOSDomainID                    = "OS_DOMAIN_ID"
	envOSDomainName                  = "OS_DOMAIN_NAME"
	envOSDefaultDomain               = "OS_DEFAULT_DOMAIN"
	envOSUserDomainID                = "OS_USER_DOMAIN_ID"
	envOSUserDomainName              = "OS_USER_DOMAIN_NAME"
	envOSProjectDomainID             = "OS_PROJECT_DOMAIN_ID"
	envOSProjectDomainName           = "OS_PROJECT_DOMAIN_NAME"
	envOSProjectID                   = "OS_PROJECT_ID"
	envOSProjectName                 = "OS_PROJECT_NAME"
	envOSPasscode                    = "OS_PASSCODE"
	envOSApplicationCredentialID     = "OS_APPLICATION_CREDENTIAL_ID"
	envOSApplicationCredentialName   = "OS_APPLICATION_CREDENTIAL_NAME"
	envOSApplicationCredentialSecret = "OS_APPLICATION_CREDENTIAL_SECRET"
	envOSSystemScope                 = "OS_SYSTEM_SCOPE"
	envOSPoolName                    = "OS_POOL_NAME"
)

// openstackEnvs contains the environment variable names we need to connect to
// OpenStack. These are only the required ones for all intalls; other env vars
// are required but it varies which ones. Gophercloud also considers:
// OS_USERID, OS_USER_ID, OS_TENANT_ID, OS_TENANT_NAME, OS_DOMAIN_ID,
// OS_DOMAIN_NAME, OS_DEFAULT_DOMAIN, OS_USER_DOMAIN_ID, OS_USER_DOMAIN_NAME,
// OS_PROJECT_DOMAIN_ID, OS_PROJECT_DOMAIN_NAME, OS_PROJECT_ID and
// OS_PROJECT_NAME (with *PROJECT* overriding *TENANT*, and only one of each
// *DOMAIN* ID/name pair being allowed to be set). We also use OS_POOL_NAME to
// determine the name of the network to get floating IPs from.
//
//nolint:gochecknoglobals // required lookup tables; an array cannot be a const
var (
	openstackReqEnvs   = [...]string{envOSAuthURL, envOSUsername, envOSPassword, envOSRegionName}
	openstackMaybeEnvs = [...]string{
		envOSUserID, envOSUserIDAlt, envOSTenantID, envOSTenantName, envOSDomainID,
		envOSDomainName, envOSDefaultDomain, envOSUserDomainID, envOSUserDomainName,
		envOSProjectDomainID, envOSProjectDomainName, envOSProjectID, envOSProjectName,
		envOSPoolName,
	}
)

var (
	errInvalidFlavorID       = errors.New(invalidFlavorIDMsg)
	errInvalidServerFlavorID = errors.New("server flavor id is not a string")
	errNoTenantOrProject     = errors.New("either OS_TENANT_ID or OS_PROJECT_ID must be set")
	errServerInErrorState    = errors.New("server is in ERROR state")
	errServerSpawnTimeout    = errors.New("timed out waiting for server to become ACTIVE")
	errServerStatusUnknown   = errors.New("server not deleted? timed out getting its status")
	errServerNotDeleted      = errors.New("server not deleted")
	errNoImageWithPrefix     = errors.New("no OS image with prefix")
)

// openstackp is our implementer of provideri.
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
	p.poolName = defaultPoolName()

	// authenticate
	ctx := context.Background()

	opts, err := openstackAuthOptionsFromEnv()
	if err != nil {
		return err
	}

	opts.AllowReauth = true

	provider, err := openstack.AuthenticatedClient(ctx, opts)
	if err != nil {
		return err
	}

	endpoint := gophercloud.EndpointOpts{
		Region: os.Getenv(envOSRegionName),
	}

	if err = p.initClients(ctx, provider, endpoint, opts); err != nil {
		return err
	}

	p.initSpawnTracking()

	return nil
}

// defaultPoolName returns the name of the network from which to get floating
// IPs, taken from OS_POOL_NAME or defaulting based on the OpenStack version.
func defaultPoolName() string {
	if poolName := os.Getenv(envOSPoolName); poolName != "" {
		return poolName
	}

	if os.Getenv(envOSTenantID) != "" {
		return "nova"
	}

	return "public"
}

func openstackAuthOptionsFromEnv() (gophercloud.AuthOptions, error) {
	authEnv := openstackAuthEnvFromEnv()
	if err := authEnv.validate(); err != nil {
		return gophercloud.AuthOptions{}, err
	}

	return gophercloud.AuthOptions{
		IdentityEndpoint:            authEnv.authURL,
		UserID:                      authEnv.userID,
		Username:                    authEnv.username,
		Password:                    authEnv.password,
		Passcode:                    authEnv.passcode,
		TenantID:                    authEnv.tenantID,
		TenantName:                  authEnv.tenantName,
		DomainID:                    authEnv.userDomainID,
		DomainName:                  authEnv.userDomainName,
		ApplicationCredentialID:     authEnv.applicationCredentialID,
		ApplicationCredentialName:   authEnv.applicationCredentialName,
		ApplicationCredentialSecret: authEnv.applicationCredentialSecret,
		Scope:                       authEnv.scope(),
	}, nil
}

// initClients creates the compute, network and image service clients (and
// resolves the tenant id), storing them on p.
func (p *openstackp) initClients(ctx context.Context, provider *gophercloud.ProviderClient,
	endpoint gophercloud.EndpointOpts, opts gophercloud.AuthOptions,
) error {
	var err error

	p.computeClient, err = openstack.NewComputeV2(provider, endpoint)
	if err != nil {
		return err
	}

	resolved, err := p.resolveTenantID(ctx, provider, endpoint, opts)
	if err != nil {
		return err
	}

	if !resolved {
		// NB: preserving long-standing behaviour: if the tenant id could not be
		// resolved (without an explicit error from project extraction), we stop
		// here without creating the network/image clients and report no error.
		return nil
	}

	p.networkClient, err = openstack.NewNetworkV2(provider, endpoint)
	if err != nil {
		return err
	}

	p.imageClient, err = openstack.NewImageV2(provider, endpoint)

	return err
}

// resolveTenantID sets p.tenantID, looking it up via the identity service if it
// is not already provided in opts. resolved is false (with a nil error) if the
// identity/token lookups failed; this mirrors the original code, which swallowed
// those errors. A genuinely missing project id is reported as an error.
func (p *openstackp) resolveTenantID(ctx context.Context, provider *gophercloud.ProviderClient,
	endpoint gophercloud.EndpointOpts, opts gophercloud.AuthOptions,
) (resolved bool, err error) {
	if opts.TenantID != "" {
		p.tenantID = opts.TenantID

		return true, nil
	}

	identityClient, erri := openstack.NewIdentityV3(provider, endpoint)
	if erri != nil {
		//nolint:nilerr // preserving long-standing behaviour of swallowing this error
		return false, nil
	}

	project, erri := tokens.Create(ctx, identityClient, &opts).ExtractProject()
	if erri != nil {
		//nolint:nilerr // preserving long-standing behaviour of swallowing this error
		return false, nil
	}

	if project.ID == "" {
		return false, errNoTenantOrProject
	}

	p.tenantID = project.ID

	return true, nil
}

// initSpawnTracking initialises the in-memory caches and the spawn-time and
// error-backoff trackers used by spawn().
func (p *openstackp) initSpawnTracking() {
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
		Factor: serverErrorBackoffFactor,
		Jitter: true,
	}

	p.createdPorts = make(map[string][]string)
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

	if found {
		return flavor, nil
	}

	// not in the cache; refresh the cache in case it was newly added
	if err := p.cacheFlavors(ctx); err != nil {
		return nil, err
	}

	p.fmapMutex.RLock()
	flavor, found = p.fmap[flavorID]
	p.fmapMutex.RUnlock()

	if !found {
		return nil, fmt.Errorf("%w: %s", errInvalidFlavorID, flavorID)
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

	return nil, fmt.Errorf("%w [%s] was found", errNoImageWithPrefix, prefix)
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
func (p *openstackp) deploy(ctx context.Context, resources *Resources, requiredPorts []int,
	useConfigDrive bool, gatewayIP, cidr string, dnsNameServers []string,
) error {
	// the resource name can only contain letters, numbers, underscores,
	// spaces and hyphens
	if !openstackValidResourceNameRegexp.MatchString(resources.ResourceName) {
		return Error{openstackName, "deploy", ErrBadResourceName}
	}

	// spawn() needs to figure out which of a server's ips are local, so we
	// parse and store the CIDR
	var err error

	_, p.ipNet, err = net.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	p.useConfigDrive = useConfigDrive

	if err = p.ensureKeyPair(ctx, resources); err != nil {
		return err
	}

	if err = p.ensureSecurityGroup(ctx, resources, requiredPorts); err != nil {
		return err
	}

	// don't create any more resources if we're already running in OpenStack
	if p.inCloud(ctx) {
		return p.configureExistingNetwork(ctx, cidr)
	}

	return p.ensureNetworkResources(ctx, resources, gatewayIP, cidr, dnsNameServers)
}

// ensureKeyPair gets, or creates if missing, the deployment's ssh key pair,
// recording its name (and any newly created private key) in resources.
func (p *openstackp) ensureKeyPair(ctx context.Context, resources *Resources) error {
	kp, err := p.getOrCreateKeyPair(ctx, resources)
	if err != nil {
		return err
	}

	resources.Details["keypair"] = kp.Name

	return nil
}

// getOrCreateKeyPair returns the existing key pair named after the deployment,
// creating a new one if it does not yet exist.
func (p *openstackp) getOrCreateKeyPair(ctx context.Context, resources *Resources) (*keypairs.KeyPair, error) {
	kp, err := keypairs.Get(ctx, p.computeClient, resources.ResourceName, nil).Extract()
	if err == nil {
		return kp, nil
	}

	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return nil, err
	}

	return p.createKeyPair(ctx, resources)
}

// createKeyPair creates a new ssh key pair for the deployment, storing the
// private key (PEM) in resources. We generate the key ourselves because recent
// OpenStack versions don't return a DER encoded key, which is what the Go
// standard library supports.
func (p *openstackp) createKeyPair(ctx context.Context, resources *Resources) (*keypairs.KeyPair, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}

	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	privateKeyPEMBytes := pem.EncodeToMemory(privateKeyPEM)

	pub, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}

	createOpts := keypairs.CreateOpts{
		Name:      resources.ResourceName,
		PublicKey: string(ssh.MarshalAuthorizedKey(pub)),
	}

	kp, err := keypairs.Create(ctx, p.computeClient, createOpts).Extract()
	if err != nil {
		return nil, err
	}

	p.createdKeyPair = true
	resources.PrivateKey = string(privateKeyPEMBytes)

	return kp, nil
}

// ensureSecurityGroup, if any ports are required, gets or creates the
// deployment's security group (opening those ports) and records it.
func (p *openstackp) ensureSecurityGroup(ctx context.Context, resources *Resources, requiredPorts []int) error {
	if len(requiredPorts) == 0 {
		return nil
	}

	group, defaultGroupExists, err := p.findSecurityGroups(ctx, resources.ResourceName)
	if err != nil {
		return err
	}

	if group == nil {
		group, err = p.createSecurityGroup(ctx, resources.ResourceName, requiredPorts)
		if err != nil {
			return err
		}
	}

	resources.Details["secgroup"] = group.ID
	p.securityGroup = resources.ResourceName
	p.hasDefaultGroup = defaultGroupExists

	return nil
}

// securityGroupSearch tracks, while paging through security groups, whether we
// have found our named group and whether a "default" group exists.
type securityGroupSearch struct {
	group              *secgroups.SecurityGroup
	resourceName       string
	foundGroup         bool
	defaultGroupExists bool
}

// consider examines one security group, updating the search state, and returns
// true once both our group and the default group have been seen (so paging can
// stop early).
func (s *securityGroupSearch) consider(g secgroups.SecurityGroup) bool {
	if g.Name == s.resourceName {
		g := g // pin
		s.group = &g
		s.foundGroup = true
	}

	if g.Name == "default" {
		s.defaultGroupExists = true
	}

	return s.foundGroup && s.defaultGroupExists
}

// findSecurityGroups looks for an existing security group named resourceName,
// also reporting whether a "default" group exists. A nil group (with nil error)
// means our group was not found and should be created.
func (p *openstackp) findSecurityGroups(ctx context.Context, resourceName string,
) (*secgroups.SecurityGroup, bool, error) {
	pager := secgroups.List(p.computeClient)
	search := &securityGroupSearch{resourceName: resourceName}

	err := pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		groupList, errf := secgroups.ExtractSecurityGroups(page)
		if errf != nil {
			return false, errf
		}

		for _, g := range groupList {
			if search.consider(g) {
				return false, nil
			}
		}

		return true, nil
	})

	return search.group, search.defaultGroupExists, err
}

// createSecurityGroup creates a new security group named resourceName, with
// rules allowing the requiredPorts (over TCP) plus ICMP.
func (p *openstackp) createSecurityGroup(ctx context.Context, resourceName string, requiredPorts []int,
) (*secgroups.SecurityGroup, error) {
	createOpts := secgroups.CreateOpts{
		Name:        resourceName,
		Description: "access amongst wr-spawned nodes",
	}

	group, err := secgroups.Create(ctx, p.computeClient, createOpts).Extract()
	if err != nil {
		return nil, err
	}

	if err = p.createSecurityGroupRules(ctx, group.ID, requiredPorts); err != nil {
		return nil, err
	}

	return group, nil
}

// createSecurityGroupRules adds rules to the given security group allowing the
// requiredPorts over TCP, plus an ICMP rule to help networking work as expected.
func (p *openstackp) createSecurityGroupRules(ctx context.Context, groupID string, requiredPorts []int) error {
	// *** check if the rules are already there, in case we previously died
	// between previous line and this one
	for _, port := range requiredPorts {
		// FromGroupID: group.ID if we were creating a head node and then
		// wanted a rule for all worker nodes...
		_, err := secgroups.CreateRule(ctx, p.computeClient, secgroups.CreateRuleOpts{
			ParentGroupID: groupID,
			FromPort:      port,
			ToPort:        port,
			IPProtocol:    "TCP",
			CIDR:          "0.0.0.0/0",
		}).Extract()
		if err != nil {
			return err
		}
	}

	// ICMP may help networking work as expected
	_, err := secgroups.CreateRule(ctx, p.computeClient, secgroups.CreateRuleOpts{
		ParentGroupID: groupID,
		FromPort:      -1,
		ToPort:        -1, // -1 results in "Any", the same as "ALL ICMP" in Horizon
		IPProtocol:    "ICMP",
		CIDR:          "0.0.0.0/0",
	}).Extract()

	return err
}

// configureExistingNetwork works out, for a wr process already running inside
// OpenStack, the network uuids it should spawn servers on (based on the cidr),
// storing them on p. Creates no new resources.
func (p *openstackp) configureExistingNetwork(ctx context.Context, cidr string) error {
	mainNetworkUUID, otherNetworkUUIDs, err := p.discoverOwnNetworks(ctx, cidr)
	if err != nil {
		return err
	}

	if mainNetworkUUID == "" {
		return Error{openstackName, "deploy", ErrBadCIDR}
	}

	p.networks = append(p.networks, servers.Network{UUID: mainNetworkUUID})
	for _, uuid := range otherNetworkUUIDs {
		p.networks = append(p.networks, servers.Network{UUID: uuid})
	}

	return nil
}

// discoverOwnNetworks classifies the networks our own server is attached to into
// the main network (whose subnet matches cidr) and any others.
func (p *openstackp) discoverOwnNetworks(ctx context.Context, cidr string,
) (mainNetworkUUID string, otherNetworkUUIDs []string, err error) {
	for networkName := range p.ownServer.Addresses {
		uuid, isMain, errc := p.classifyOwnNetwork(ctx, networkName, cidr)
		if errc != nil {
			return "", nil, errc
		}

		switch {
		case uuid == "":
			continue
		case isMain:
			mainNetworkUUID = uuid
		default:
			otherNetworkUUIDs = append(otherNetworkUUIDs, uuid)
		}
	}

	return mainNetworkUUID, otherNetworkUUIDs, nil
}

// classifyOwnNetwork resolves a network name our server is on to its uuid, and
// reports whether it is the main network (its subnet matches cidr). A "" uuid
// means the name didn't resolve and should be skipped.
func (p *openstackp) classifyOwnNetwork(ctx context.Context, networkName, cidr string,
) (uuid string, isMain bool, err error) {
	networkUUID, err := p.networkIDFromName(ctx, networkName)
	if err != nil {
		return "", false, err
	}

	if networkUUID == "" {
		return "", false, nil
	}

	isMain, err = p.networkMatchesCIDR(ctx, networkUUID, networkName, cidr)
	if err != nil {
		return "", false, err
	}

	return networkUUID, isMain, nil
}

// networkMatchesCIDR reports whether the given network has a subnet with the
// given cidr; if so it also records the network as our network name.
func (p *openstackp) networkMatchesCIDR(ctx context.Context, networkUUID, networkName, cidr string) (bool, error) {
	network, err := networks.Get(ctx, p.networkClient, networkUUID).Extract()
	if err != nil {
		return false, err
	}

	for _, subnetID := range network.Subnets {
		subnet, errg := subnets.Get(ctx, p.networkClient, subnetID).Extract()
		if errg != nil {
			return false, errg
		}

		if subnet.CIDR == cidr {
			p.networkName = networkName

			return true, nil
		}
	}

	return false, nil
}

// ensureNetworkResources gets or creates the network, subnet and router needed
// to spawn servers when wr is not already running inside OpenStack.
func (p *openstackp) ensureNetworkResources(ctx context.Context, resources *Resources,
	gatewayIP, cidr string, dnsNameServers []string,
) error {
	network, networkID, err := p.ensureNetwork(ctx, resources)
	if err != nil {
		return err
	}

	resources.Details["network"] = networkID
	p.networkName = resources.ResourceName
	p.networks = append(p.networks, servers.Network{UUID: networkID})

	subnetID, err := p.ensureSubnet(ctx, resources, network, networkID, gatewayIP, cidr, dnsNameServers)
	if err != nil {
		return err
	}

	resources.Details["subnet"] = subnetID

	routerID, err := p.ensureRouter(ctx, resources, subnetID)
	if err != nil {
		return err
	}

	resources.Details["router"] = routerID

	return nil
}

// ensureNetwork gets, or creates if missing, the deployment's network, also
// returning its id.
func (p *openstackp) ensureNetwork(ctx context.Context, resources *Resources) (*networks.Network, string, error) {
	networkID, err := p.networkIDFromName(ctx, resources.ResourceName)
	if err != nil {
		var notFound gophercloud.ErrResourceNotFound
		if !errors.As(err, &notFound) {
			return nil, "", err
		}

		return p.createNetwork(ctx, resources)
	}

	network, err := networks.Get(ctx, p.networkClient, networkID).Extract()
	if err != nil {
		return nil, "", err
	}

	return network, networkID, nil
}

// createNetwork creates a new network for the deployment, returning it and its
// id.
func (p *openstackp) createNetwork(ctx context.Context, resources *Resources) (*networks.Network, string, error) {
	createOpts := networks.CreateOpts{
		Name:         resources.ResourceName,
		AdminStateUp: enabledBool(),
	}

	network, err := networks.Create(ctx, p.networkClient, createOpts).Extract()
	if err != nil {
		return nil, "", err
	}

	return network, network.ID, nil
}

// ensureSubnet returns the id of the network's existing single subnet, or
// creates a big enough subnet (with the given gateway, cidr and DNS servers).
func (p *openstackp) ensureSubnet(ctx context.Context, resources *Resources, network *networks.Network,
	networkID, gatewayIP, cidr string, dnsNameServers []string,
) (string, error) {
	if len(network.Subnets) == 1 {
		// *** check it's valid? could we end up with more than 1 subnet?
		return network.Subnets[0], nil
	}

	// add a big enough subnet
	gip := new(string)
	*gip = gatewayIP

	subnet, err := subnets.Create(ctx, p.networkClient, subnets.CreateOpts{
		NetworkID: networkID,
		CIDR:      cidr,
		GatewayIP: gip,
		// DNSNameservers is critical, or servers on new networks can't be ssh'd
		// to for many minutes
		DNSNameservers: dnsNameServers,
		IPVersion:      ipVersion4,
		Name:           resources.ResourceName,
	}).Extract()
	if err != nil {
		return "", err
	}

	return subnet.ID, nil
}

// ensureRouter returns the id of the deployment's existing router, or creates
// one (attached to the external network and our subnet).
func (p *openstackp) ensureRouter(ctx context.Context, resources *Resources, subnetID string) (string, error) {
	routerID, err := p.existingRouterID(ctx, resources.ResourceName)
	if err != nil {
		return "", err
	}

	if routerID != "" {
		return routerID, nil
	}

	return p.createRouter(ctx, resources, subnetID)
}

// existingRouterID returns the id of an existing router named resourceName, or
// "" if there isn't one.
func (p *openstackp) existingRouterID(ctx context.Context, resourceName string) (string, error) {
	var routerID string

	pager := routers.List(p.networkClient, routers.ListOpts{Name: resourceName})

	err := pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		routerList, errf := routers.ExtractRouters(page)
		if errf != nil {
			return false, errf
		}

		routerID = routerList[0].ID
		// *** check it's valid? could we end up with more than 1 router?
		return false, nil
	})

	return routerID, err
}

// createRouter creates a router named resourceName, attached to the external
// network, and adds our subnet to it.
func (p *openstackp) createRouter(ctx context.Context, resources *Resources, subnetID string) (string, error) {
	// get the external network id
	if p.externalNetworkID == "" {
		externalNetworkID, err := p.networkIDFromName(ctx, p.poolName)
		if err != nil {
			return "", err
		}

		p.externalNetworkID = externalNetworkID
	}

	router, err := routers.Create(ctx, p.networkClient, routers.CreateOpts{
		Name:         resources.ResourceName,
		GatewayInfo:  &routers.GatewayInfo{NetworkID: p.externalNetworkID},
		AdminStateUp: enabledBool(),
	}).Extract()
	if err != nil {
		return "", err
	}

	// add our subnet
	_, err = routers.AddInterface(ctx, p.networkClient, router.ID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract()
	if err != nil {
		// if this fails, we'd be stuck with a useless router, so we try and
		// delete it
		routers.Delete(ctx, p.networkClient, router.ID)

		return "", err
	}

	return router.ID, nil
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
			if details, ok := p.matchingServerDetails(ctx, resources, server); ok {
				sdetails = append(sdetails, details)
			}
		}

		return true, nil
	})

	return sdetails, err
}

// matchingServerDetails returns the [id, ip, name, adminPass] details of the
// given server if it belongs to this deployment (name prefix match, not our own
// server) and its ip can be determined; ok is false otherwise.
func (p *openstackp) matchingServerDetails(ctx context.Context, resources *Resources,
	server servers.Server,
) ([]string, bool) {
	if p.ownName == server.Name || !strings.HasPrefix(server.Name, resources.ResourceName) {
		return nil, false
	}

	serverIP, err := p.getServerIP(ctx, server.ID)
	if err != nil {
		return nil, false
	}

	return []string{server.ID, serverIP, server.Name, server.AdminPass}, true
}

// inCloud checks if we're currently running on an OpenStack server based on our
// hostname matching a host in OpenStack.
func (p *openstackp) inCloud(ctx context.Context) bool {
	hostname, err := os.Hostname()
	if err != nil {
		return false
	}

	inCloud := false
	pager := servers.List(p.computeClient, servers.ListOpts{})

	err = pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		serverList, errf := servers.ExtractServers(page)
		if errf != nil {
			return false, errf
		}

		if p.recordOwnServer(serverList, hostname) {
			inCloud = true

			return false, nil
		}

		return true, nil
	})
	if err != nil {
		clog.Warn(ctx, "paging through servers failed", "err", err)
	}

	return inCloud
}

// recordOwnServer looks for the server in serverList whose name maps to the
// given hostname; if found it records it as our own server and returns true.
func (p *openstackp) recordOwnServer(serverList []servers.Server, hostname string) bool {
	for _, server := range serverList {
		if nameToHostName(server.Name) == hostname {
			p.ownName = hostname
			server := server // pin (not needed since we return, but just to be careful)
			p.ownServer = &server

			return true
		}
	}

	return false
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
	maps.Copy(fmap, p.fmap)
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

	err = p.addUsedQuota(ctx, quota)

	return quota, err
}

// addUsedQuota queries all servers and adds their resource usage to quota.
func (p *openstackp) addUsedQuota(ctx context.Context, quota *Quota) error {
	// query all servers to figure out what we've used of our quota
	// (*** gophercloud currently doesn't implement getting this properly)
	err := p.cacheFlavors(ctx)
	if err != nil {
		clog.Warn(ctx, "failed to cache available flavors", "err", err)
	}

	pager := servers.List(p.computeClient, servers.ListOpts{})

	return pager.EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
		serverList, errf := servers.ExtractServers(page)
		if errf != nil {
			return false, errf
		}

		for _, server := range serverList {
			if errf = p.addServerToUsedQuota(ctx, quota, server); errf != nil {
				return false, errf
			}
		}

		return true, nil
	})
}

// addServerToUsedQuota adds the given server's instance, core and RAM usage to
// quota. A flavor that no longer exists is tolerated (logged), since we iterate
// over all servers, not just ones we created.
func (p *openstackp) addServerToUsedQuota(ctx context.Context, quota *Quota, server servers.Server) error {
	quota.UsedInstances++

	flavorID, ok := server.Flavor["id"].(string)
	if !ok {
		return fmt.Errorf("%w: %s", errInvalidServerFlavorID, server.ID)
	}

	f, err := p.getFlavor(ctx, flavorID)
	if err != nil {
		if !errors.Is(err, errInvalidFlavorID) {
			return err
		}

		warnStr := "an old server has a flavor that no longer exists; our remaining quota estimation will be off"
		clog.Warn(ctx, warnStr, "server", server.ID, "flavor", flavorID)
	}

	if f != nil {
		quota.UsedCores += f.Cores
		quota.UsedRAM += f.RAM
	}
	// *** how to find out how much volume storage this is using?...

	return nil
}

// resolveSpawnImageAndFlavor looks up the image (by osPrefix) and flavor (by
// flavorID) for a spawn, also raising diskGB to the image's minimum if needed.
func (p *openstackp) resolveSpawnImageAndFlavor(ctx context.Context, osPrefix, flavorID string, diskGB int,
) (*imageimages.Image, *Flavor, int, error) {
	// get the image that matches desired OS
	image, err := p.getImage(ctx, osPrefix)
	if err != nil {
		return nil, nil, diskGB, err
	}

	flavor, err := p.getFlavor(ctx, flavorID)
	if err != nil {
		return nil, nil, diskGB, err
	}

	// if the OS image itself specifies a minimum disk size and it's higher than
	// requested disk, increase our requested disk
	if image.MinDiskGigabytes > diskGB {
		diskGB = image.MinDiskGigabytes
	}

	return image, flavor, diskGB, nil
}

// spawn achieves the aims of Spawn().
func (p *openstackp) spawn(ctx context.Context, resources *Resources, osPrefix string, flavorID string,
	diskGB int, externalIP bool, usingQuotaCh chan bool,
) (serverID, serverIP, serverName, adminPass string, err error) {
	image, flavor, diskGB, err := p.resolveSpawnImageAndFlavor(ctx, osPrefix, flavorID, diskGB)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	server, serverName, err := p.createAndWaitForServer(ctx, resources, image, flavorID, flavor, diskGB, usingQuotaCh)
	if server != nil {
		serverID = server.ID
	}

	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	adminPass = server.AdminPass

	// *** NB. it can still take some number of seconds before I can ssh to it
	serverIP, err = p.assignServerIP(ctx, serverID, externalIP)
	if err != nil {
		return serverID, serverIP, serverName, adminPass, err
	}

	p.attachExtraNetworkPorts(ctx, resources, serverID)

	return serverID, serverIP, serverName, adminPass, nil
}

// createAndWaitForServer backs off after prior failures, requests the new
// server (signalling quota usage on usingQuotaCh), and waits for it to become
// ACTIVE. It returns the server (which may be non-nil even on error) and its
// name.
func (p *openstackp) createAndWaitForServer(ctx context.Context, resources *Resources,
	image *imageimages.Image, flavorID string, flavor *Flavor, diskGB int, usingQuotaCh chan bool,
) (*servers.Server, string, error) {
	p.waitForPriorSpawnFailure(ctx)

	server, serverName, createdVolume, err := p.createServer(ctx, resources, image, flavorID, flavor, diskGB)

	serverID := ""
	if server != nil {
		serverID = server.ID
	}

	usingQuotaCh <- true

	if err != nil {
		p.setSpawnFailed(true)

		return server, serverName, err
	}

	if err = p.waitForServerActive(ctx, server, serverID, createdVolume); err != nil {
		return server, serverName, err
	}

	return server, serverName, nil
}

// waitForPriorSpawnFailure sleeps (backing off) if a previous spawn failed, to
// avoid hammering OpenStack with repeated failing requests.
func (p *openstackp) waitForPriorSpawnFailure(ctx context.Context) {
	p.spMutex.RLock()
	sf := p.spawnFailed
	p.spMutex.RUnlock()

	if sf {
		wait := p.errorBackoff.Duration()
		clog.Warn(ctx, "server spawn waiting due to prior failures", "wait", wait)
		time.Sleep(wait)
	}
}

// setSpawnFailed records whether the most recent spawn failed; on a success
// following a failure it also resets the error backoff.
func (p *openstackp) setSpawnFailed(failed bool) {
	p.spMutex.Lock()
	defer p.spMutex.Unlock()

	if !failed && p.spawnFailed {
		p.errorBackoff.Reset()
	}

	p.spawnFailed = failed
}

// createServer issues the request to create a new server with a unique name,
// creating a volume if the requested disk is larger than the flavor's root
// disk. It returns the server, its name and whether a volume was created.
func (p *openstackp) createServer(ctx context.Context, resources *Resources, image *imageimages.Image,
	flavorID string, flavor *Flavor, diskGB int,
) (server *servers.Server, serverName string, createdVolume bool, err error) {
	serverName, createOpts, createdVolume := p.buildServerCreateOpts(resources, image, flavorID, flavor, diskGB)

	t := time.Now()
	server, err = servers.Create(ctx, p.computeClient, keypairs.CreateOptsExt{
		CreateOptsBuilder: createOpts,
		KeyName:           resources.ResourceName,
	}, nil).Extract()

	serverID := ""
	if server != nil {
		serverID = server.ID
	}

	clog.Debug(ctx, "server create attempted", "took", time.Since(t), "id", serverID, "worked", err == nil)

	return server, serverName, createdVolume, err
}

// buildServerCreateOpts builds the options for creating a new server with a
// unique name, requesting a volume if the desired disk exceeds the flavor's
// root disk. It returns the chosen name and whether a volume was requested.
func (p *openstackp) buildServerCreateOpts(resources *Resources, image *imageimages.Image, flavorID string,
	flavor *Flavor, diskGB int,
) (serverName string, createOpts servers.CreateOpts, createdVolume bool) {
	serverName = uniqueResourceName(resources.ResourceName)
	createOpts = servers.CreateOpts{
		Name:           serverName,
		FlavorRef:      flavorID,
		ImageRef:       image.ID,
		SecurityGroups: p.securityGroupsForSpawn(),
		Networks:       []servers.Network{p.networks[0]},
		ConfigDrive:    &p.useConfigDrive,
		UserData:       sentinelInitScript,
	}

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
		createdVolume = true
	}

	return serverName, createOpts, createdVolume
}

// securityGroupsForSpawn returns the security groups to apply to a new server:
// the one we created (if any) plus the "default" group if it exists.
func (p *openstackp) securityGroupsForSpawn() []string {
	var secGroups []string
	if p.securityGroup != "" {
		secGroups = append(secGroups, p.securityGroup)
		if p.hasDefaultGroup {
			secGroups = append(secGroups, "default")
		}
	}

	return secGroups
}

// waitForServerActive waits for the just-created server to reach ACTIVE status
// (servers.WaitForStatus has a timeout, but it doesn't always work, so we roll
// our own). On failure it records the spawn as failed and tries to delete the
// bad server.
func (p *openstackp) waitForServerActive(ctx context.Context, server *servers.Server, serverID string,
	createdVolume bool,
) error {
	waitForActive := make(chan error)
	go p.pollServerUntilActive(ctx, serverID, server, createdVolume, waitForActive)

	err := <-waitForActive
	if err != nil {
		// since we're going to return an error that we failed to spawn, try and
		// delete the bad server in case it is still there
		p.setSpawnFailed(true)

		delerr := servers.Delete(ctx, p.computeClient, server.ID).ExtractErr()
		if delerr != nil {
			err = fmt.Errorf("%w\nadditionally, there was an error deleting the bad server: %w", err, delerr)
		}

		return err
	}

	p.setSpawnFailed(false)

	return nil
}

// pollServerUntilActive polls the server's status once a second until it is
// ACTIVE (recording the spawn time), goes to ERROR, or a learned timeout
// elapses, sending the outcome on waitForActive. Intended to be run in a
// goroutine.
func (p *openstackp) pollServerUntilActive(ctx context.Context, serverID string, server *servers.Server,
	createdVolume bool, waitForActive chan error,
) {
	defer internal.LogPanic(ctx, "spawn", false)

	timeoutS, typical := p.spawnTimeout(createdVolume)
	timeout := time.After(time.Duration(timeoutS) * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	start := time.Now()
	attempts := 0

	for {
		select {
		case <-ticker.C:
			attempts++

			if done, derr := p.pollServerStatusTick(ctx, serverID, createdVolume, start, attempts); done {
				ticker.Stop()

				waitForActive <- derr

				return
			}
		case <-timeout:
			ticker.Stop()

			waitForActive <- p.spawnTimeoutError(ctx, serverID, server, typical, start)

			return
		}
	}
}

// pollServerStatusTick performs a single poll of the server's status. When done
// is true, err is the outcome to report (nil if the server became ACTIVE).
func (p *openstackp) pollServerStatusTick(ctx context.Context, serverID string, createdVolume bool,
	start time.Time, attempts int,
) (done bool, err error) {
	current, errf := servers.Get(ctx, p.computeClient, serverID).Extract()
	if errf != nil {
		return true, errf
	}

	return p.assessServerStatus(ctx, current, serverID, createdVolume, start, attempts)
}

// spawnTimeout returns how long (in seconds) to wait for a server to become
// ACTIVE, based on recent spawn times, and the typical recent spawn time.
func (p *openstackp) spawnTimeout(createdVolume bool) (timeoutS float64, typical int) {
	p.stMutex.RLock()

	if createdVolume {
		timeoutS = p.spawnTimesVolume.Value() * spawnTimeoutMultiplier
		typical = int(p.spawnTimesVolume.Value())
	} else {
		timeoutS = p.spawnTimes.Value() * spawnTimeoutMultiplier
		typical = int(p.spawnTimes.Value())
	}

	p.stMutex.RUnlock()

	if timeoutS <= 0 {
		timeoutS = initialServerSpawnTimeout.Seconds()
	}

	if timeoutS < minimumServerSpawnTimeoutSecs {
		timeoutS = minimumServerSpawnTimeoutSecs
	}

	return timeoutS, typical
}

// assessServerStatus decides, from a polled server, whether polling should stop.
// When done is true, err is the outcome (nil if the server became ACTIVE). On
// becoming ACTIVE it records the spawn time.
func (p *openstackp) assessServerStatus(ctx context.Context, current *servers.Server, serverID string,
	createdVolume bool, start time.Time, attempts int,
) (done bool, err error) {
	switch current.Status {
	case "ACTIVE":
		clog.Debug(ctx, "server became ACTIVE", "id", serverID, "took", time.Since(start), "polls", attempts)
		p.recordSpawnTime(time.Since(start).Seconds(), createdVolume)

		return true, nil
	case "ERROR":
		msg := current.Fault.Message
		if msg == "" {
			msg = "unknown problem"
		}

		return true, fmt.Errorf("%w: server %s after %s and %d polls: %s",
			errServerInErrorState, serverID, time.Since(start), attempts, msg)
	default:
		return false, nil
	}
}

// recordSpawnTime adds the given spawn duration (seconds) to the appropriate
// moving average.
func (p *openstackp) recordSpawnTime(spawnSecs float64, createdVolume bool) {
	p.stMutex.Lock()
	defer p.stMutex.Unlock()

	if createdVolume {
		p.spawnTimesVolume.Add(spawnSecs)
	} else {
		p.spawnTimes.Add(spawnSecs)
	}
}

// spawnTimeoutError builds the error returned when a server fails to become
// ACTIVE within the timeout, including its current status if obtainable.
func (p *openstackp) spawnTimeoutError(ctx context.Context, serverID string, server *servers.Server,
	typical int, start time.Time,
) error {
	current, errf := servers.Get(ctx, p.computeClient, serverID).Extract()

	status := "unknown"
	if errf == nil {
		status = current.Status
	}

	return fmt.Errorf(
		"%w: server %s is %s after %ds (typical time to becoming active has been %ds)",
		errServerSpawnTimeout, server.ID, status, int(time.Since(start).Seconds()), typical)
}

// assignServerIP gives the server a floating (external) ip or finds its
// internal ip. On any error it first destroys the now-useless server.
func (p *openstackp) assignServerIP(ctx context.Context, serverID string, externalIP bool) (string, error) {
	if !externalIP {
		serverIP, err := p.getServerIP(ctx, serverID)
		if err != nil {
			p.destroyServerAfterIPError(ctx, serverID, "server destruction after not finding ip")

			return serverIP, err
		}

		return serverIP, nil
	}

	return p.assignFloatingIP(ctx, serverID)
}

// assignFloatingIP gets a floating ip and associates it with the server's port.
// On any error it first destroys the now-useless server.
func (p *openstackp) assignFloatingIP(ctx context.Context, serverID string) (string, error) {
	// give it a floating ip
	floatingIP, err := p.getAvailableFloatingIP(ctx)
	if err != nil {
		p.destroyServerAfterIPError(ctx, serverID, "server destruction after no IP failed")

		return "", err
	}

	// associate floating ip with server *** we have a race condition
	// between finding/creating free floating IP above, and using it here
	portID, err := p.getServerPortID(ctx, serverID)
	if err != nil {
		p.destroyServerAfterIPError(ctx, serverID, "server destruction after not finding port failed")

		return "", err
	}

	_, err = networkfloatingips.Update(ctx, p.networkClient, floatingIP.ID, networkfloatingips.UpdateOpts{
		PortID: &portID,
	}).Extract()
	if err != nil {
		p.destroyServerAfterIPError(ctx, serverID, "server destruction after not associating IP failed")

		return "", err
	}

	return floatingIP.FloatingIP, nil
}

// destroyServerAfterIPError destroys a server that we failed to give an ip to,
// logging (under warnMsg) any failure of the destruction itself.
func (p *openstackp) destroyServerAfterIPError(ctx context.Context, serverID, warnMsg string) {
	if errd := p.destroyServer(ctx, serverID); errd != nil {
		clog.Warn(ctx, warnMsg, "server", serverID, "err", errd)
	}
}

// attachExtraNetworkPorts, when the deployment spans multiple networks, creates
// and attaches a port on each network other than the first. Failures are logged
// and skipped rather than returned.
func (p *openstackp) attachExtraNetworkPorts(ctx context.Context, resources *Resources, serverID string) {
	if len(p.networks) <= 1 {
		return
	}

	for i, network := range p.networks {
		if i == 0 {
			continue
		}

		p.attachNetworkPort(ctx, resources, serverID, network, i)
	}
}

// attachNetworkPort creates a port on the given network and attaches it to the
// server, logging and skipping on failure.
func (p *openstackp) attachNetworkPort(ctx context.Context, resources *Resources, serverID string,
	network servers.Network, i int,
) {
	portCreateOtps := ports.CreateOpts{
		AdminStateUp: enabledBool(),
		NetworkID:    network.UUID,
		Name:         fmt.Sprintf("%s-%s-%d", resources.ResourceName, serverID, i),
	}

	port, err := ports.Create(ctx, p.networkClient, portCreateOtps).Extract()
	if err != nil {
		clog.Warn(ctx, "failed to create port", "err", err, "network", network.UUID)

		return
	}

	p.createdPorts[serverID] = append(p.createdPorts[serverID], port.ID)

	attachOpts := attachinterfaces.CreateOpts{
		PortID: port.ID,
	}

	_, err = attachinterfaces.Create(ctx, p.computeClient, serverID, attachOpts).Extract()
	if err != nil {
		clog.Warn(ctx, "failed to attach port", "err", err, "network", network.UUID, "port", port.ID, "server", serverID)

		return
	}

	clog.Debug(ctx, "attached port for extra network", "server", serverID, "network", network.UUID, "port", port.ID)
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

	server, err := p.waitForServerDeletion(ctx, serverID)
	if err == nil {
		err = fmt.Errorf("%w, still has status '%s'", errServerNotDeleted, server.Status)
	}

	if errorIsNotFound(err) {
		err = nil
	}

	p.deleteCreatedPorts(ctx, serverID)

	return err
}

// waitForServerDeletion waits (up to destroyServerTimeout) for the server to
// actually be gone, which is needed before its router and network can be
// deleted. We poll for a "resource not found" error rather than using
// servers.WaitForStatus, which could force us to wait on the timeout. A nil
// error means the server still exists (its details are returned).
func (p *openstackp) waitForServerDeletion(ctx context.Context, serverID string) (*servers.Server, error) {
	limit := time.After(destroyServerTimeout)
	ticker := time.NewTicker(destroyServerCheckFrequency)

	var (
		server *servers.Server
		err    error
	)

	for {
		select {
		case <-ticker.C:
			var done bool

			server, done, err = p.checkServerGone(ctx, serverID, limit)
			if done {
				ticker.Stop()

				return server, err
			}
		case <-limit:
			ticker.Stop()

			return server, err
		}
	}
}

// checkServerGone does a single (timeout-protected) status check of the server.
// done is true once we should stop waiting (the Get returned an error, or the
// overall limit elapsed mid-check).
func (p *openstackp) checkServerGone(ctx context.Context, serverID string, limit <-chan time.Time,
) (server *servers.Server, done bool, err error) {
	// servers.Get() call can get stuck for a long time, so let that time out as
	// well
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

		return server, err != nil, err
	case <-limit:
		return server, true, errServerStatusUnknown
	}
}

// deleteCreatedPorts deletes any extra-network ports we created for the server,
// logging (but not failing on) errors.
func (p *openstackp) deleteCreatedPorts(ctx context.Context, serverID string) {
	createdPorts, created := p.createdPorts[serverID]
	if !created {
		return
	}

	for _, uuid := range createdPorts {
		errP := ports.Delete(ctx, p.networkClient, uuid).ExtractErr()
		if errP != nil {
			clog.Warn(ctx, "failed to delete a port", "id", uuid, "server", serverID)
		}
	}

	delete(p.createdPorts, serverID)
}

// tearDown achieves the aims of TearDown().
func (p *openstackp) tearDown(ctx context.Context, resources *Resources) error {
	// throughout we'll ignore errors because we want to try and delete
	// as much as possible; we'll end up returning a concatenation of all of
	// them though
	var merr *multierror.Error

	// delete servers, except for ourselves
	toDestroy, err := p.serversToDestroy(ctx, resources)
	merr = p.combineError(merr, err)

	didSomething := len(toDestroy) > 0
	if didSomething {
		p.destroyServersInParallel(ctx, toDestroy)
	}

	if p.ownName == "" {
		merr = p.tearDownNetworkResources(ctx, resources, merr, &didSomething)
	}

	merr = p.tearDownKeyPair(ctx, resources, merr)

	rerr := merr.ErrorOrNil()
	if rerr == nil && !didSomething {
		return Error{openstackName, "tearDown", ErrNoTearDown}
	}

	return rerr
}

// serversToDestroy returns the ids of all servers belonging to this deployment
// (name prefix match), other than our own server.
func (p *openstackp) serversToDestroy(ctx context.Context, resources *Resources) ([]string, error) {
	var toDestroy []string

	pager := servers.List(p.computeClient, servers.ListOpts{})
	err := pager.EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
		serverList, errf := servers.ExtractServers(page)
		if errf != nil {
			return false, errf
		}

		for _, server := range serverList {
			if p.ownName != server.Name && strings.HasPrefix(server.Name, resources.ResourceName) {
				toDestroy = append(toDestroy, server.ID)
			}
		}

		return true, nil
	})

	return toDestroy, err
}

// destroyServersInParallel destroys all the given servers concurrently, logging
// (but not failing on) individual destruction errors.
func (p *openstackp) destroyServersInParallel(ctx context.Context, toDestroy []string) {
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

// tearDownNetworkResources deletes the router, network and security group (only
// done when we are not running inside OpenStack). It accumulates errors on merr
// and sets *didSomething when it deletes a credential-specific resource.
func (p *openstackp) tearDownNetworkResources(ctx context.Context, resources *Resources,
	merr *multierror.Error, didSomething *bool,
) *multierror.Error {
	merr = p.tearDownRouter(ctx, resources, merr, didSomething)
	merr = p.tearDownResource(ctx, resources.Details["network"], merr, didSomething,
		"delete network (auto-deletes subnet)", func() error {
			return networks.Delete(ctx, p.networkClient, resources.Details["network"]).ExtractErr()
		})
	merr = p.tearDownResource(ctx, resources.Details["secgroup"], merr, didSomething,
		"delete security group", func() error {
			return secgroups.Delete(ctx, p.computeClient, resources.Details["secgroup"]).ExtractErr()
		})

	return merr
}

// tearDownResource deletes a single resource identified by id (a no-op if id is
// empty), logging it under debugMsg, accumulating any error on merr and setting
// *didSomething on success.
func (p *openstackp) tearDownResource(ctx context.Context, id string, merr *multierror.Error,
	didSomething *bool, debugMsg string, del func() error,
) *multierror.Error {
	if id == "" {
		return merr
	}

	t := time.Now()
	err := del()
	clog.Debug(ctx, debugMsg, "time", time.Since(t), "id", id, "err", err)

	if err == nil {
		*didSomething = true
	}

	return p.combineError(merr, err)
}

// tearDownRouter removes our subnet's interface from, and then deletes, the
// router (if any).
func (p *openstackp) tearDownRouter(ctx context.Context, resources *Resources, merr *multierror.Error,
	didSomething *bool,
) *multierror.Error {
	id := resources.Details["router"]
	if id == "" {
		return merr
	}

	if subnetid := resources.Details["subnet"]; subnetid != "" {
		merr = p.removeRouterInterface(ctx, id, subnetid, merr)
	}

	return p.tearDownResource(ctx, id, merr, didSomething, "delete router", func() error {
		return routers.Delete(ctx, p.networkClient, id).ExtractErr()
	})
}

// removeRouterInterface removes the subnet's interface from the router, retrying
// for a few seconds since destroyed servers may not have fully terminated yet.
func (p *openstackp) removeRouterInterface(ctx context.Context, routerID, subnetID string,
	merr *multierror.Error,
) *multierror.Error {
	tries := 0

	for {
		t := time.Now()
		removeOpts := routers.RemoveInterfaceOpts{SubnetID: subnetID}
		_, errr := routers.RemoveInterface(ctx, p.networkClient, routerID, removeOpts).Extract()
		clog.Debug(ctx, "remove router interface", "time", time.Since(t), "routerid",
			routerID, "subnetid", subnetID, "err", errr)

		if errr == nil {
			return merr
		}

		tries++
		if tries >= maxRouterInterfaceRemovalTries {
			return p.combineError(merr, errr)
		}

		<-time.After(1 * time.Second)
	}
}

// tearDownKeyPair deletes the deployment's key pair, unless we're running in
// OpenStack and the security group and keypair share a resource name (meaning
// our current server needs the same keypair we used to spawn our servers). The
// exception is bypassed if we definitely created the key pair this session.
func (p *openstackp) tearDownKeyPair(ctx context.Context, resources *Resources, merr *multierror.Error,
) *multierror.Error {
	id := resources.Details["keypair"]
	if id == "" {
		return merr
	}

	if !p.createdKeyPair && p.ownName != "" && (p.securityGroup == "" || p.securityGroup == id) {
		return merr
	}

	t := time.Now()
	err := keypairs.Delete(ctx, p.computeClient, id, nil).ExtractErr()
	clog.Debug(ctx, "delete keypair", "time", time.Since(t), "id", id, "err", err)
	// keypairs are not credential-specific enough, so we don't consider
	// deleting one as didSomething
	resources.PrivateKey = ""

	return p.combineError(merr, err)
}

// combineError Append()s the given err on merr, but ignores err if it is
// "Resource not found".
func (p *openstackp) combineError(merr *multierror.Error, err error) *multierror.Error {
	if err != nil && !errorIsNotFound(err) {
		merr = multierror.Append(merr, err)
	}

	return merr
}

// getAvailableFloatingIP gets or creates an unused floating ip.
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

type openstackAuthEnv struct {
	authURL                     string
	username                    string
	userID                      string
	password                    string
	passcode                    string
	tenantID                    string
	tenantName                  string
	domainID                    string
	domainName                  string
	defaultDomain               string
	userDomainID                string
	userDomainName              string
	projectDomainID             string
	projectDomainName           string
	applicationCredentialID     string
	applicationCredentialName   string
	applicationCredentialSecret string
	systemScope                 string
}

func openstackAuthEnvFromEnv() openstackAuthEnv {
	authEnv := baseOpenStackAuthEnvFromEnv()
	authEnv.applyProjectOverrides()
	authEnv.applyDomainFallbacks()

	return authEnv
}

func baseOpenStackAuthEnvFromEnv() openstackAuthEnv {
	return openstackAuthEnv{
		authURL:                     os.Getenv(envOSAuthURL),
		username:                    os.Getenv(envOSUsername),
		userID:                      firstOpenStackEnv(envOSUserID, envOSUserIDAlt),
		password:                    os.Getenv(envOSPassword),
		passcode:                    os.Getenv(envOSPasscode),
		tenantID:                    os.Getenv(envOSTenantID),
		tenantName:                  os.Getenv(envOSTenantName),
		domainID:                    os.Getenv(envOSDomainID),
		domainName:                  os.Getenv(envOSDomainName),
		defaultDomain:               os.Getenv(envOSDefaultDomain),
		userDomainID:                os.Getenv(envOSUserDomainID),
		userDomainName:              os.Getenv(envOSUserDomainName),
		projectDomainID:             os.Getenv(envOSProjectDomainID),
		projectDomainName:           os.Getenv(envOSProjectDomainName),
		applicationCredentialID:     os.Getenv(envOSApplicationCredentialID),
		applicationCredentialName:   os.Getenv(envOSApplicationCredentialName),
		applicationCredentialSecret: os.Getenv(envOSApplicationCredentialSecret),
		systemScope:                 os.Getenv(envOSSystemScope),
	}
}

func (authEnv *openstackAuthEnv) applyProjectOverrides() {
	if projectID := os.Getenv(envOSProjectID); projectID != "" {
		authEnv.tenantID = projectID
	}

	if projectName := os.Getenv(envOSProjectName); projectName != "" {
		authEnv.tenantName = projectName
	}
}

func (authEnv *openstackAuthEnv) applyDomainFallbacks() {
	authEnv.userDomainID = fallbackOpenStackEnv(authEnv.userDomainID, authEnv.domainID)
	authEnv.projectDomainID = fallbackOpenStackEnv(authEnv.projectDomainID, authEnv.domainID)
	authEnv.userDomainName = fallbackOpenStackEnv(authEnv.userDomainName, authEnv.domainName)
	authEnv.projectDomainName = fallbackOpenStackEnv(authEnv.projectDomainName, authEnv.domainName)
	authEnv.applyDefaultDomainFallbacks()
}

func fallbackOpenStackEnv(currentValue, fallbackValue string) string {
	if currentValue != "" {
		return currentValue
	}

	return fallbackValue
}

func (authEnv *openstackAuthEnv) applyDefaultDomainFallbacks() {
	if authEnv.defaultDomain == "" {
		return
	}

	if authEnv.userDomainID == "" && authEnv.userDomainName == "" {
		authEnv.userDomainID = authEnv.defaultDomain
	}

	if authEnv.projectDomainID == "" && authEnv.projectDomainName == "" {
		authEnv.projectDomainID = authEnv.defaultDomain
	}
}

func (authEnv openstackAuthEnv) validate() error {
	if authEnv.authURL == "" {
		return gophercloud.ErrMissingEnvironmentVariable{EnvironmentVariable: envOSAuthURL}
	}

	if authEnv.missingUser() {
		return gophercloud.ErrMissingAnyoneOfEnvironmentVariables{
			EnvironmentVariables: []string{envOSUserID, envOSUsername},
		}
	}

	if authEnv.missingPasswordAuth() {
		return gophercloud.ErrMissingEnvironmentVariable{EnvironmentVariable: envOSPassword}
	}

	if authEnv.missingApplicationCredentialSecret() {
		return gophercloud.ErrMissingEnvironmentVariable{EnvironmentVariable: envOSApplicationCredentialSecret}
	}

	if authEnv.usesApplicationCredentialName() {
		return authEnv.validateApplicationCredentialName()
	}

	return nil
}

func (authEnv openstackAuthEnv) missingUser() bool {
	return authEnv.userID == "" &&
		authEnv.username == "" &&
		authEnv.applicationCredentialID == "" &&
		authEnv.applicationCredentialSecret == ""
}

func (authEnv openstackAuthEnv) missingPasswordAuth() bool {
	return authEnv.password == "" &&
		authEnv.passcode == "" &&
		authEnv.applicationCredentialID == "" &&
		authEnv.applicationCredentialName == ""
}

func (authEnv openstackAuthEnv) missingApplicationCredentialSecret() bool {
	return (authEnv.applicationCredentialID != "" || authEnv.applicationCredentialName != "") &&
		authEnv.applicationCredentialSecret == ""
}

func (authEnv openstackAuthEnv) usesApplicationCredentialName() bool {
	return authEnv.applicationCredentialID == "" &&
		authEnv.applicationCredentialName != "" &&
		authEnv.applicationCredentialSecret != ""
}

func (authEnv openstackAuthEnv) validateApplicationCredentialName() error {
	if authEnv.userID == "" && authEnv.username == "" {
		return gophercloud.ErrMissingAnyoneOfEnvironmentVariables{
			EnvironmentVariables: []string{envOSUserID, envOSUsername},
		}
	}

	if authEnv.username != "" && authEnv.userDomainID == "" && authEnv.userDomainName == "" {
		return gophercloud.ErrMissingAnyoneOfEnvironmentVariables{
			EnvironmentVariables: []string{envOSUserDomainID, envOSUserDomainName},
		}
	}

	return nil
}

func (authEnv openstackAuthEnv) scope() *gophercloud.AuthScope {
	if authEnv.systemScope == "all" {
		return &gophercloud.AuthScope{System: true}
	}

	if authEnv.tenantID != "" {
		return &gophercloud.AuthScope{ProjectID: authEnv.tenantID}
	}

	if authEnv.tenantName != "" {
		return &gophercloud.AuthScope{
			ProjectName: authEnv.tenantName,
			DomainID:    authEnv.projectDomainID,
			DomainName:  authEnv.projectDomainName,
		}
	}

	return nil
}

func firstOpenStackEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}

	return ""
}
