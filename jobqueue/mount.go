// Copyright © 2016-2017 Genome Research Limited
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

package jobqueue

// This file contains the mount related code.

import (
	"bytes"
	"encoding/json"
	"sort"
)

// MountConfig struct is used for setting in a Job to specify that a remote file
// system or object store should be fuse mounted prior to running the Job's Cmd.
// Currently only supports S3-like object stores.
type MountConfig struct {
	// Mount is the local directory on which to mount your Target(s). It can be
	// (in) any directory you're able to write to. If the directory doesn't
	// exist, it will be created first. Otherwise, it must be empty. If not
	// supplied, defaults to the subdirectory "mnt" in the Job's working
	// directory if CwdMatters, otherwise the actual working directory will be
	// used as the mount point.
	Mount string `json:",omitempty"`

	// CacheBase is the parent directory to use for the CacheDir of any Targets
	// configured with Cache on, but CacheDir undefined, or specified with a
	// relative path. If CacheBase is also undefined, the base will be the Job's
	// Cwd if CwdMatters, otherwise it will be the parent of the Job's actual
	// working directory.
	CacheBase string `json:",omitempty"`

	// Retries is the number of retries that should be attempted when
	// encountering errors in trying to access your remote S3 bucket. At least 3
	// is recommended. It defaults to 10 if not provided.
	Retries int `json:",omitempty"`

	// Verbose is a boolean, which if true, would cause timing information on
	// all remote S3 calls to appear as lines of all job STDERR that use the
	// mount. Errors always appear there.
	Verbose bool `json:",omitempty"`

	// Targets is a slice of MountTarget which define what you want to access at
	// your Mount. It's a slice to allow you to multiplex different buckets (or
	// different subdirectories of the same bucket) so that it looks like all
	// their data is in the same place, for easier access to files in your
	// mount. You can only have one of these configured to be writeable.
	Targets []MountTarget
}

// MountTarget struct is used for setting in a MountConfig to define what you
// want to access at your Mount.
type MountTarget struct {
	// Profile is the S3 configuration profile name to use. If not supplied, the
	// value of the $AWS_DEFAULT_PROFILE or $AWS_PROFILE environment variables
	// is used, and if those are unset it defaults to "default".
	//
	// We look at number of standard S3 configuration files and environment
	// variables to determine the scheme, domain, region and authentication
	// details to connect to S3 with. All possible sources are checked to fill
	// in any missing values from more preferred sources.
	//
	// The preferred file is ~/.s3cfg, since this is the only config file type
	// that allows the specification of a custom domain. This file is Amazon's
	// s3cmd config file, described here: http://s3tools.org/kb/item14.htm. wr
	// will look at the access_key, secret_key, use_https and host_base options
	// under the section with the given Profile name. If you don't wish to use
	// any other config files or environment variables, you can add the non-
	// standard region option to this file if you need to specify a specific
	// region.
	//
	// The next file checked is the one pointed to by the
	// $AWS_SHARED_CREDENTIALS_FILE environment variable, or ~/.aws/credentials.
	// This file is described here:
	// http://docs.aws.amazon.com/cli/latest/userguide/cli-chap-getting-
	// started.html. wr will look at the aws_access_key_id and
	// aws_secret_access_key options under the section with the given Profile
	// name.
	//
	// wr also checks the file pointed to by the $AWS_CONFIG_FILE environment
	// variable, or ~/.aws/config, described in the previous link. From here the
	// region option is used from the section with the given Profile name. If
	// you don't wish to use a ~/.s3cfg file but do need to specify a custom
	// domain, you can add the non-standard host_base and use_https options to
	// this file instead.
	//
	// As a last resort, ~/.awssecret is checked. This is s3fs's config file,
	// and consists of a single line with your access key and secret key
	// separated by a colon.
	//
	// If set, the environment variables $AWS_ACCESS_KEY_ID,
	// $AWS_SECRET_ACCESS_KEY and $AWS_DEFAULT_REGION override corresponding
	// options found in any config file.
	Profile string `json:",omitempty"`

	// Path (required) is the name of your S3 bucket, optionally followed URL-
	// style (separated with forward slashes) by sub-directory names. The
	// highest performance is gained by specifying the deepest path under your
	// bucket that holds all the files you wish to access.
	Path string

	// Cache is a boolean, which if true, turns on data caching of any data
	// retrieved, or any data you wish to upload.
	Cache bool `json:",omitempty"`

	// CacheDir is the local directory to store cached data. If this parameter
	// is supplied, Cache is forced true and so doesn't need to be provided. If
	// this parameter is not supplied but Cache is true, the directory will be a
	// unique directory in the containing MountConfig's CacheBase, and will get
	// deleted on unmount. If it's a relative path, it will be relative to the
	// CacheBase.
	CacheDir string `json:",omitempty"`

	// Write is a boolean, which if true, makes the mount point writeable. If
	// you don't intend to write to a mount, just leave this parameter out.
	// Because writing currently requires caching, turning this on forces Cache
	// to be considered true.
	Write bool `json:",omitempty"`
}

// MountConfigs is a slice of MountConfig.
type MountConfigs []MountConfig

// String provides a JSON representation of the MountConfigs.
func (mcs MountConfigs) String() string {
	if len(mcs) == 0 {
		return ""
	}
	b, _ := json.Marshal(mcs)
	return string(b)
}

// Key returns a string representation of the most critical parts of the config
// that would make it different from other MountConfigs in practical terms of
// what files are accessible from where: only Mount, Target.Profile and
// Target.Path are considered. The order of Targets (but not of MountConfig) is
// considered as well.
func (mcs MountConfigs) Key() string {
	if len(mcs) == 0 {
		return ""
	}

	// sort mcs first, since the order doesn't affect what files are available
	// where
	if len(mcs) > 1 {
		sort.Slice(mcs, func(i, j int) bool {
			return mcs[i].Mount < mcs[j].Mount
		})
	}

	var key bytes.Buffer
	for _, mc := range mcs {
		mount := mc.Mount
		if mount == "" {
			mount = "mnt"
		}
		key.WriteString(mount)
		key.WriteString(":")

		for _, t := range mc.Targets {
			profile := t.Profile
			if profile == "" {
				profile = "default"
			}
			key.WriteString(profile)
			key.WriteString("-")
			key.WriteString(t.Path)
			key.WriteString(";")
		}
	}

	return key.String()
}
