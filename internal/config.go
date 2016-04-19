// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

// Package internal houses code for vrpipe's general utility functions
package internal

import (
	"fmt"
	"github.com/jinzhu/configor"
	"os"
	"path/filepath"
)

const (
	ConfigCommonBasename = ".vrpipe_config.yml"
)

type Config struct {
	Daemon_port string `default:"11301"`
	Daemon_host string `default:"localhost"`
}

/*
ConfigLoad loads configuration settings from files and environment
variables.

We prefer settings in config file in current dir (or the current dir's parent
dir if the useparentdir option is true (used for test scripts)) over config file
in home directory over config file in dir pointed to by VRPIPE_CONFIG_DIR.

The deployment argument determines if we read .vrpipe_config.production.yml or
.vrpipe_config.development.yml; we always read .vrpipe_config.yml. If the empty
string is supplied, deployment is development if you're in the git repository
directory. Otherwise, deployment is taken from the environment variable
VRPIPE_DEPLOYMENT, and if that's not set it defaults to production.

Multiple of these files can be used to have settings that are common to
multiple users and deployments, and settings specific to users or deployments.

Settings found in no file can be set with the environment variable
VRPIPE_<setting name in caps>, eg.
export VRPIPE_DAEMON_PORT="11301"
*/
func ConfigLoad(deployment string, useparentdir bool) Config {
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if useparentdir {
		pwd = filepath.Dir(pwd)
	}

	// if deployment not set on the command line
	if deployment != "development" && deployment != "production" {
		// if we're in the git repository
		if _, err := os.Stat(filepath.Join(pwd, "main.go")); err == nil {
			// force development
			deployment = "development"
		} else {
			// default to production
			deployment = "production"

			// and allow env var to override with development
			if deploymentEnv := os.Getenv("VRPIPE_DEPLOYMENT"); deploymentEnv != "" {
				if deploymentEnv == "development" {
					deployment = "development"
				}
			}
		}
	}
	os.Setenv("CONFIGOR_ENV", deployment)
	os.Setenv("CONFIGOR_ENV_PREFIX", "VRPIPE")
	ConfigDeploymentBasename := ".vrpipe_config." + deployment + ".yml"

	// read the config files. We have to check file existence before passing
	// these to configor.Load, or it will complain
	var configFiles []string
	configFile := filepath.Join(pwd, ConfigCommonBasename)
	_, err = os.Stat(configFile)
	if _, err2 := os.Stat(filepath.Join(pwd, ConfigDeploymentBasename)); err == nil || err2 == nil {
		configFiles = append(configFiles, configFile)
	}
	if home := os.Getenv("HOME"); home != "" {
		configFile = filepath.Join(home, ConfigCommonBasename)
		_, err = os.Stat(configFile)
		if _, err2 := os.Stat(filepath.Join(home, ConfigDeploymentBasename)); err == nil || err2 == nil {
			configFiles = append(configFiles, configFile)
		}
	}
	if configDir := os.Getenv("VRPIPE_CONFIG_DIR"); configDir != "" {
		configFile = filepath.Join(configDir, ConfigCommonBasename)
		_, err = os.Stat(configFile)
		if _, err2 := os.Stat(filepath.Join(configDir, ConfigDeploymentBasename)); err == nil || err2 == nil {
			configFiles = append(configFiles, configFile)
		}
	}

	config := Config{}
	configor.Load(&config, configFiles...)

	return config
}
