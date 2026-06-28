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
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	testOpenStackAuthURL       = "https://openstack.example:5000/v3"
	testOpenStackUsername      = "jdoe"
	testOpenStackPassword      = "secret"
	testOpenStackDefaultDomain = "default"
)

func TestOpenStackMaybeEnvListsDomainCompatibilityNames(t *testing.T) {
	Convey("OpenStack MaybeEnv lists user and project domain compatibility variables", t, func() {
		vars, err := MaybeEnv("openstack")
		So(err, ShouldBeNil)

		envSet := make(map[string]bool, len(vars))
		for _, name := range vars {
			envSet[name] = true
		}

		So(envSet[envOSUserDomainID], ShouldBeTrue)
		So(envSet[envOSUserDomainName], ShouldBeTrue)
		So(envSet[envOSProjectDomainID], ShouldBeTrue)
		So(envSet[envOSProjectDomainName], ShouldBeTrue)
		So(envSet[envOSDefaultDomain], ShouldBeTrue)
	})
}

func TestOpenStackAuthOptionsFromEnv(t *testing.T) {
	Convey("OpenStack auth options accept user-domain env vars with project ID scope", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:        testOpenStackAuthURL,
			envOSUsername:       testOpenStackUsername,
			envOSPassword:       testOpenStackPassword,
			envOSProjectID:      "project-id",
			envOSTenantID:       "tenant-id",
			envOSUserDomainName: "users",
		})

		opts, err := openstackAuthOptionsFromEnv()

		So(err, ShouldBeNil)
		So(opts.TenantID, ShouldEqual, "project-id")
		assertAuthOptionsBuildTokenV3Maps(opts)
		So(opts.DomainName, ShouldEqual, "users")
		So(opts.Scope, ShouldNotBeNil)
		So(opts.Scope.ProjectID, ShouldEqual, "project-id")
	})

	Convey("OpenStack auth options keep user and project domains separate for project-name scope", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:           testOpenStackAuthURL,
			envOSUsername:          testOpenStackUsername,
			envOSPassword:          testOpenStackPassword,
			envOSProjectName:       "analysis",
			envOSTenantName:        "tenant",
			envOSUserDomainName:    "users",
			envOSProjectDomainName: "projects",
		})

		opts, err := openstackAuthOptionsFromEnv()

		So(err, ShouldBeNil)
		So(opts.TenantName, ShouldEqual, "analysis")
		So(opts.DomainName, ShouldEqual, "users")
		So(opts.Scope, ShouldNotBeNil)
		So(opts.Scope.ProjectName, ShouldEqual, "analysis")
		So(opts.Scope.DomainName, ShouldEqual, "projects")
		assertAuthOptionsBuildTokenV3Maps(opts)
	})

	Convey("OpenStack auth options still accept generic domain env vars", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:    testOpenStackAuthURL,
			envOSUsername:   testOpenStackUsername,
			envOSPassword:   testOpenStackPassword,
			envOSProjectID:  "project-id",
			envOSDomainName: testOpenStackDefaultDomain,
		})

		opts, err := openstackAuthOptionsFromEnv()

		So(err, ShouldBeNil)
		So(opts.DomainName, ShouldEqual, testOpenStackDefaultDomain)
		assertAuthOptionsBuildTokenV3Maps(opts)
	})

	Convey("OpenStack auth options use the default domain as a fallback", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:       testOpenStackAuthURL,
			envOSUsername:      testOpenStackUsername,
			envOSPassword:      testOpenStackPassword,
			envOSProjectName:   "analysis",
			envOSDefaultDomain: testOpenStackDefaultDomain,
		})

		opts, err := openstackAuthOptionsFromEnv()

		So(err, ShouldBeNil)
		So(opts.DomainID, ShouldEqual, testOpenStackDefaultDomain)
		So(opts.Scope, ShouldNotBeNil)
		So(opts.Scope.ProjectName, ShouldEqual, "analysis")
		So(opts.Scope.DomainID, ShouldEqual, testOpenStackDefaultDomain)
		assertAuthOptionsBuildTokenV3Maps(opts)
	})
}

func setOpenStackAuthTestEnv(t *testing.T, values map[string]string) {
	t.Helper()

	for _, name := range []string{
		envOSAuthURL,
		envOSUsername,
		envOSPassword,
		envOSUserID,
		envOSUserIDAlt,
		envOSTenantID,
		envOSTenantName,
		envOSDomainID,
		envOSDomainName,
		envOSDefaultDomain,
		envOSUserDomainID,
		envOSUserDomainName,
		envOSProjectDomainID,
		envOSProjectDomainName,
		envOSProjectID,
		envOSProjectName,
		envOSPasscode,
		envOSApplicationCredentialID,
		envOSApplicationCredentialName,
		envOSApplicationCredentialSecret,
		envOSSystemScope,
	} {
		t.Setenv(name, "")
	}

	for name, value := range values {
		t.Setenv(name, value)
	}
}

func assertAuthOptionsBuildTokenV3Maps(opts gophercloud.AuthOptions) {
	scope, err := opts.ToTokenV3ScopeMap()
	So(err, ShouldBeNil)

	_, err = opts.ToTokenV3CreateMap(scope)
	So(err, ShouldBeNil)
}
