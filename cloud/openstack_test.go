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
	"errors"
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

func TestOpenStackProviderEnvLists(t *testing.T) {
	Convey("OpenStack RequiredEnv only lists variables required for every accepted auth mode", t, func() {
		vars, err := RequiredEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackRequiredEnv())
	})

	Convey("OpenStack MaybeEnv lists all accepted conditional auth and provider variables", t, func() {
		vars, err := MaybeEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackMaybeEnv())
	})

	Convey("OpenStack AllEnv combines required and conditional variables", t, func() {
		vars, err := AllEnv("openstack")
		So(err, ShouldBeNil)
		So(vars, ShouldResemble, expectedOpenStackAllEnv())
	})

	Convey("OpenStack required env check accepts application credential auth without username or password", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:                     testOpenStackAuthURL,
			envOSApplicationCredentialID:     "app-id",
			envOSApplicationCredentialSecret: testOpenStackPassword,
		})
		t.Setenv(envOSRegionName, "RegionOne")

		provider := &Provider{impl: &openstackp{}}

		So(provider.checkRequiredEnv(), ShouldBeNil)
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

	Convey("OpenStack auth options list all accepted user ID variables when user is missing", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:  testOpenStackAuthURL,
			envOSPassword: testOpenStackPassword,
		})

		_, err := openstackAuthOptionsFromEnv()

		assertMissingOpenStackUserEnv(err)
	})

	Convey("OpenStack credential-name auth lists all accepted user variables when user is missing", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:                     testOpenStackAuthURL,
			envOSApplicationCredentialName:   testOpenStackUsername,
			envOSApplicationCredentialSecret: testOpenStackPassword,
		})

		_, err := openstackAuthOptionsFromEnv()

		assertMissingOpenStackUserEnv(err)
	})

	Convey("OpenStack application credential secret alone still requires a user identifier", t, func() {
		setOpenStackAuthTestEnv(t, map[string]string{
			envOSAuthURL:                     testOpenStackAuthURL,
			envOSApplicationCredentialSecret: testOpenStackPassword,
		})

		_, err := openstackAuthOptionsFromEnv()

		assertMissingOpenStackUserEnv(err)
	})
}

func expectedOpenStackAllEnv() []string {
	envs := append([]string{}, expectedOpenStackRequiredEnv()...)

	return append(envs, expectedOpenStackMaybeEnv()...)
}

func expectedOpenStackRequiredEnv() []string {
	return []string{envOSAuthURL, envOSRegionName}
}

func expectedOpenStackMaybeEnv() []string {
	return []string{
		envOSUserID, envOSUserIDAlt, envOSUsername, envOSPassword, envOSPasscode,
		envOSTenantID, envOSTenantName, envOSDomainID, envOSDomainName, envOSDefaultDomain,
		envOSUserDomainID, envOSUserDomainName, envOSProjectDomainID, envOSProjectDomainName,
		envOSProjectID, envOSProjectName, envOSApplicationCredentialID, envOSApplicationCredentialName,
		envOSApplicationCredentialSecret, envOSSystemScope, envOSPoolName,
	}
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

func assertMissingOpenStackUserEnv(err error) {
	var missingErr gophercloud.ErrMissingAnyoneOfEnvironmentVariables

	So(errors.As(err, &missingErr), ShouldBeTrue)
	So(missingErr.EnvironmentVariables, ShouldResemble, []string{
		envOSUserID,
		envOSUserIDAlt,
		envOSUsername,
	})
}
