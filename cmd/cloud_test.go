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

package cmd

import (
	"testing"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

func TestCloudDeployDebugManagerStartFlags(t *testing.T) {
	oldCloudDebug := cloudDebug

	t.Cleanup(func() {
		cloudDebug = oldCloudDebug
	})

	Convey("cloud deploy debug uses supported manager start flags", t, func() {
		cloudDebug = true

		So(buildDebugArg(), ShouldEqual, " --debug --runner_syslog")
		So(buildDebugArg(), ShouldNotContainSubstring, "--runner_debug")
	})
}

func TestBuildManagerStartCmdDebugFlags(t *testing.T) {
	oldConfig := config
	oldProviderName := providerName
	oldCloudMaxServers := cloudMaxServers
	oldServerKeepAlive := serverKeepAlive
	oldOSPrefix := osPrefix
	oldOSUsername := osUsername
	oldOSRAM := osRAM
	oldOSDisk := osDisk
	oldFlavorRegex := flavorRegex
	oldFlavorSets := flavorSets
	oldPostCreationScript := postCreationScript
	oldPreDestroyScript := preDestroyScript
	oldCloudSpawns := cloudSpawns
	oldCloudCIDR := cloudCIDR
	oldCloudConfigFiles := cloudConfigFiles
	oldCloudDebug := cloudDebug
	oldCloudManagerTimeoutSeconds := cloudManagerTimeoutSeconds
	oldCloudResourceNameUniquer := cloudResourceNameUniquer
	oldMaxManagerCores := maxManagerCores
	oldMaxManagerRAM := maxManagerRAM
	oldCloudServersAutoConfirmDead := cloudServersAutoConfirmDead
	oldMountJSON := mountJSON

	t.Cleanup(func() {
		config = oldConfig
		providerName = oldProviderName
		cloudMaxServers = oldCloudMaxServers
		serverKeepAlive = oldServerKeepAlive
		osPrefix = oldOSPrefix
		osUsername = oldOSUsername
		osRAM = oldOSRAM
		osDisk = oldOSDisk
		flavorRegex = oldFlavorRegex
		flavorSets = oldFlavorSets
		postCreationScript = oldPostCreationScript
		preDestroyScript = oldPreDestroyScript
		cloudSpawns = oldCloudSpawns
		cloudCIDR = oldCloudCIDR
		cloudConfigFiles = oldCloudConfigFiles
		cloudDebug = oldCloudDebug
		cloudManagerTimeoutSeconds = oldCloudManagerTimeoutSeconds
		cloudResourceNameUniquer = oldCloudResourceNameUniquer
		maxManagerCores = oldMaxManagerCores
		maxManagerRAM = oldMaxManagerRAM
		cloudServersAutoConfirmDead = oldCloudServersAutoConfirmDead
		mountJSON = oldMountJSON
	})

	Convey("wr cloud deploy --debug builds a valid remote manager start command", t, func() {
		config = &internal.Config{Deployment: "debug-test"}
		providerName = "openstack"
		cloudMaxServers = 3
		serverKeepAlive = 7
		osPrefix = "Ubuntu 24"
		osUsername = "ubuntu"
		osRAM = 2048
		osDisk = 0
		flavorRegex = ""
		flavorSets = ""
		postCreationScript = ""
		preDestroyScript = ""
		cloudSpawns = 2
		cloudCIDR = "192.168.64.0/18"
		cloudConfigFiles = ""
		cloudDebug = true
		cloudManagerTimeoutSeconds = 60
		cloudResourceNameUniquer = "testuser"
		maxManagerCores = -1
		maxManagerRAM = -1
		cloudServersAutoConfirmDead = 0
		mountJSON = ""

		cmd := buildManagerStartCmd(&cloud.Provider{}, cloud.NewServer("ubuntu", "192.0.2.1", ""), "/tmp/wr", false, false)

		So(cmd, ShouldContainSubstring, "/tmp/wr manager start")
		So(cmd, ShouldContainSubstring, " --debug")
		So(cmd, ShouldContainSubstring, " --runner_syslog")
		So(cmd, ShouldNotContainSubstring, "--runner_debug")
	})
}
