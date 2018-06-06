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

package internal

// this file implements the config system used by the cmd package

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/inconshreveable/log15"
	"github.com/jinzhu/configor"
)

const (
	configCommonBasename = ".wr_config.yml"

	// S3Prefix is the prefix used by S3 paths
	S3Prefix = "s3://"

	// Production is the name of the main deployment
	Production = "production"

	// Development is the name of the development deployment, used during testing
	Development = "development"
)

// Config holds the configuration options for jobqueue server and client
type Config struct {
	ManagerPort         string `default:""`
	ManagerWeb          string `default:""`
	ManagerHost         string `default:"localhost"`
	ManagerDir          string `default:"~/.wr"`
	ManagerPidFile      string `default:"pid"`
	ManagerLogFile      string `default:"log"`
	ManagerDbFile       string `default:"db"`
	ManagerDbBkFile     string `default:"db_bk"`
	ManagerTokenFile    string `default:"client.token"`
	ManagerUploadDir    string `default:"uploads"`
	ManagerUmask        int    `default:"007"`
	ManagerScheduler    string `default:"local"`
	ManagerCAFile       string `default:"ca.pem"`
	ManagerCertFile     string `default:"cert.pem"`
	ManagerKeyFile      string `default:"key.pem"`
	ManagerCertDomain   string `default:"localhost"`
	ManagerSetDomainIP  bool   `default:"false"`
	RunnerExecShell     string `default:"bash"`
	Deployment          string `default:"production"`
	CloudFlavor         string `default:""`
	CloudKeepAlive      int    `default:"120"`
	CloudServers        int    `default:"-1"`
	CloudCIDR           string `default:"192.168.0.0/18"`
	CloudGateway        string `default:"192.168.0.1"`
	CloudDNS            string `default:"8.8.4.4,8.8.8.8"`
	CloudOS             string `default:"Ubuntu Xenial"`
	ContainerImage      string `default:"ubuntu:latest"`
	CloudUser           string `default:"ubuntu"`
	CloudRAM            int    `default:"2048"`
	CloudDisk           int    `default:"1"`
	CloudScript         string `default:""`
	CloudConfigFiles    string `default:"~/.s3cfg,~/.aws/credentials,~/.aws/config"`
	DeploySuccessScript string `default:""`
}

/*
ConfigLoad loads configuration settings from files and environment
variables. Note, this function exits on error, since without config we can't
do anything.

We prefer settings in config file in current dir (or the current dir's parent
dir if the useparentdir option is true (used for test scripts)) over config file
in home directory over config file in dir pointed to by WR_CONFIG_DIR.

The deployment argument determines if we read .wr_config.production.yml or
.wr_config.development.yml; we always read .wr_config.yml. If the empty
string is supplied, deployment is development if you're in the git repository
directory. Otherwise, deployment is taken from the environment variable
WR_DEPLOYMENT, and if that's not set it defaults to production.

Multiple of these files can be used to have settings that are common to
multiple users and deployments, and settings specific to users or deployments.

Settings found in no file can be set with the environment variable
WR_<setting name in caps>, eg.
export WR_MANAGER_PORT="11301"
*/
func ConfigLoad(deployment string, useparentdir bool, logger log15.Logger) Config {
	pwd, err := os.Getwd()
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	if useparentdir {
		pwd = filepath.Dir(pwd)
	}

	// if deployment not set on the command line
	if deployment != Development && deployment != Production {
		deployment = DefaultDeployment(logger)
	}
	err = os.Setenv("CONFIGOR_ENV", deployment)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	err = os.Setenv("CONFIGOR_ENV_PREFIX", "WR")
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	ConfigDeploymentBasename := ".wr_config." + deployment + ".yml"

	// read the config files. We have to check file existence before passing
	// these to configor.Load, or it will complain
	var configFiles []string
	configFile := filepath.Join(pwd, configCommonBasename)
	_, err = os.Stat(configFile)
	if _, err2 := os.Stat(filepath.Join(pwd, ConfigDeploymentBasename)); err == nil || err2 == nil {
		configFiles = append(configFiles, configFile)
	}
	home := os.Getenv("HOME")
	if home != "" {
		configFile = filepath.Join(home, configCommonBasename)
		_, err = os.Stat(configFile)
		if _, err2 := os.Stat(filepath.Join(home, ConfigDeploymentBasename)); err == nil || err2 == nil {
			configFiles = append(configFiles, configFile)
		}
	}
	if configDir := os.Getenv("WR_CONFIG_DIR"); configDir != "" {
		configFile = filepath.Join(configDir, configCommonBasename)
		_, err = os.Stat(configFile)
		if _, err2 := os.Stat(filepath.Join(configDir, ConfigDeploymentBasename)); err == nil || err2 == nil {
			configFiles = append(configFiles, configFile)
		}
	}

	config := Config{}
	err = configor.Load(&config, configFiles...)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	config.Deployment = deployment

	// convert the possible ~/ in Manager_dir to abs path to user's home
	config.ManagerDir = TildaToHome(config.ManagerDir)
	config.ManagerDir += "_" + deployment

	// create the manager dir now, or else we're doomed to failure
	err = os.MkdirAll(config.ManagerDir, 0700)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// convert the possible relative paths in Manager_*_file to abs paths in
	// ManagerDir
	if !filepath.IsAbs(config.ManagerPidFile) {
		config.ManagerPidFile = filepath.Join(config.ManagerDir, config.ManagerPidFile)
	}
	if !filepath.IsAbs(config.ManagerLogFile) {
		config.ManagerLogFile = filepath.Join(config.ManagerDir, config.ManagerLogFile)
	}
	if !filepath.IsAbs(config.ManagerDbFile) {
		config.ManagerDbFile = filepath.Join(config.ManagerDir, config.ManagerDbFile)
	}
	if !IsRemote(config.ManagerDbBkFile) && !filepath.IsAbs(config.ManagerDbBkFile) {
		config.ManagerDbBkFile = filepath.Join(config.ManagerDir, config.ManagerDbBkFile)
	}
	if !filepath.IsAbs(config.ManagerCAFile) {
		config.ManagerCAFile = filepath.Join(config.ManagerDir, config.ManagerCAFile)
	}
	if !filepath.IsAbs(config.ManagerCertFile) {
		config.ManagerCertFile = filepath.Join(config.ManagerDir, config.ManagerCertFile)
	}
	if !filepath.IsAbs(config.ManagerKeyFile) {
		config.ManagerKeyFile = filepath.Join(config.ManagerDir, config.ManagerKeyFile)
	}
	if !filepath.IsAbs(config.ManagerTokenFile) {
		config.ManagerTokenFile = filepath.Join(config.ManagerDir, config.ManagerTokenFile)
	}
	if !filepath.IsAbs(config.ManagerUploadDir) {
		config.ManagerUploadDir = filepath.Join(config.ManagerDir, config.ManagerUploadDir)
	}

	// if not explicitly set, calculate ports that no one else would be
	// assigned by us (and hope no other software is using it...)
	if config.ManagerPort == "" {
		config.ManagerPort = calculatePort(config.Deployment, "cli", logger)
	}
	if config.ManagerWeb == "" {
		config.ManagerWeb = calculatePort(config.Deployment, "webi", logger)
	}

	return config
}

// IsProduction tells you if we're in the production deployment.
func (c Config) IsProduction() bool {
	return c.Deployment == Production
}

// IsDevelopment tells you if we're in the development deployment.
func (c Config) IsDevelopment() bool {
	return c.Deployment == Development
}

// DefaultDeployment works out the default deployment.
func DefaultDeployment(logger log15.Logger) string {
	pwd, err := os.Getwd()
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// if we're in the git repository
	var deployment string
	if _, err := os.Stat(filepath.Join(pwd, "jobqueue", "server.go")); err == nil {
		// force development
		deployment = Development
	} else {
		// default to production
		deployment = Production

		// and allow env var to override with development
		if deploymentEnv := os.Getenv("WR_DEPLOYMENT"); deploymentEnv != "" {
			if deploymentEnv == Development {
				deployment = Development
			}
		}
	}
	return deployment
}

// DefaultConfig works out the default config for when we need to be able to
// report the default before we know what deployment the user has actually
// chosen, ie. before we have a final config.
func DefaultConfig(logger log15.Logger) Config {
	return ConfigLoad(DefaultDeployment(logger), false, logger)
}

// DefaultServer works out the default server (we need this to be able to report
// this default before we know what deployment the user has actually chosen, ie.
// before we have a final config).
func DefaultServer(logger log15.Logger) (server string) {
	config := DefaultConfig(logger)
	return config.ManagerHost + ":" + config.ManagerPort
}

// Calculate a port number that will be unique to this user, deployment and
// ptype ("cli" or "webi").
func calculatePort(deployment string, ptype string, logger log15.Logger) (port string) {
	uid, err := Userid()
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// our port must be greater than 1024, and by basing on user id we can
	// avoid conflicts with other users of wr on the same machine; we
	// multiply by 4 because we have to reserve 4 ports for each user
	pn := 1021 + (uid * 4)

	// maximum port number is 65535
	if pn+3 > 65535 {
		logger.Error("Could not calculate a suitable unique port number for you, since your user id is so large; please manually set your manager_port and manager_web config options.")
		os.Exit(1)
	}

	if deployment == Development {
		pn += 2
	}
	if ptype == "webi" {
		pn++
	}

	// it's easier for the things that use this port number if it's a string
	// (because it's used as part of a connection string)
	return strconv.Itoa(pn)
}

// InS3 tells you if a path is to a file in S3.
func InS3(path string) bool {
	return strings.HasPrefix(path, S3Prefix)
}

// IsRemote tells you if a path is to a remote file system or object store,
// based on its URI.
func IsRemote(path string) bool {
	// (right now we only support S3, but IsRemote is to future-proof us and
	// avoid calling InS3() directly)
	return InS3(path)
}
