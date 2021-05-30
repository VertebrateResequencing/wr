// Copyright © 2016-2019 Genome Research Limited
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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/creasty/defaults"
	"github.com/inconshreveable/log15"
	"github.com/jinzhu/configor"
	"github.com/manifoldco/promptui"
	"github.com/olekukonko/tablewriter"
	"github.com/wtsi-ssg/wr/network/port"
)

const (
	configCommonBasename = ".wr_config.yml"

	// S3Prefix is the prefix used by S3 paths
	S3Prefix = "s3://"

	// Production is the name of the main deployment
	Production = "production"

	// Development is the name of the development deployment, used during testing
	Development = "development"

	// ConfigSourceEnvVar is a config value source
	ConfigSourceEnvVar = "env var"

	// ConfigSourceDefault is a config value source
	ConfigSourceDefault = "default"

	sourcesProperty = "sources"

	portsNeeded      = 4
	portsProdDevDiff = 2
	portsCmdWebDiff  = 1
	portsMinPort     = 1021
	portsMaxPort     = 65535
)

// Config holds the configuration options for jobqueue server and client
type Config struct {
	ManagerPort          string `default:""`
	ManagerWeb           string `default:""`
	ManagerHost          string `default:"localhost"`
	ManagerDir           string `default:"~/.wr"`
	ManagerPidFile       string `default:"pid"`
	ManagerLogFile       string `default:"log"`
	ManagerDbFile        string `default:"db"`
	ManagerDbBkFile      string `default:"db_bk"`
	ManagerTokenFile     string `default:"client.token"`
	ManagerUploadDir     string `default:"uploads"`
	ManagerUmask         int    `default:"007"`
	ManagerScheduler     string `default:"local"`
	ManagerCAFile        string `default:"ca.pem"`
	ManagerCertFile      string `default:"cert.pem"`
	ManagerKeyFile       string `default:"key.pem"`
	ManagerCertDomain    string `default:"localhost"`
	ManagerSetDomainIP   bool   `default:"false"`
	RunnerExecShell      string `default:"bash"`
	PrivateKeyPath       string `default:"~/.ssh/id_rsa"`
	Deployment           string `default:"production"`
	CloudFlavor          string `default:""`
	CloudFlavorManager   string `default:""`
	CloudFlavorSets      string `default:""`
	CloudKeepAlive       int    `default:"120"`
	CloudServers         int    `default:"-1"`
	CloudCIDR            string `default:"192.168.64.0/18"`
	CloudGateway         string `default:"192.168.64.1"`
	CloudDNS             string `default:"8.8.4.4,8.8.8.8"`
	CloudOS              string `default:"bionic-server"`
	ContainerImage       string `default:"ubuntu:latest"`
	CloudUser            string `default:"ubuntu"`
	CloudRAM             int    `default:"2048"`
	CloudDisk            int    `default:"1"`
	CloudScript          string `default:""`
	CloudConfigFiles     string `default:"~/.s3cfg,~/.aws/credentials,~/.aws/config"`
	CloudSpawns          int    `default:"10"`
	CloudAutoConfirmDead int    `default:"30"`
	DeploySuccessScript  string `default:""`
	sources              map[string]string
}

// merge compares existing to new Config values, and for each one that has
// changed, sets the given source on the changed property in our sources,
// and sets the new value on ourselves.
func (c *Config) merge(new *Config, source string) {
	v := reflect.ValueOf(*c)
	typeOfC := v.Type()
	vNew := reflect.ValueOf(*new)

	if c.sources == nil {
		c.sources = make(map[string]string)
	}

	for i := 0; i < v.NumField(); i++ {
		property := typeOfC.Field(i).Name
		if property == sourcesProperty {
			continue
		}

		if vNew.Field(i).Interface() != v.Field(i).Interface() {
			c.sources[property] = source

			adrField := reflect.ValueOf(c).Elem().Field(i)
			switch typeOfC.Field(i).Type.Kind() {
			case reflect.String:
				adrField.SetString(vNew.Field(i).String())
			case reflect.Int:
				adrField.SetInt(vNew.Field(i).Int())
			case reflect.Bool:
				adrField.SetBool(vNew.Field(i).Bool())
			}
		}
	}
}

// clone makes a new Config with our values.
func (c *Config) clone() *Config {
	new := &Config{}

	v := reflect.ValueOf(*c)
	typeOfC := v.Type()
	for i := 0; i < v.NumField(); i++ {
		property := typeOfC.Field(i).Name
		if property == sourcesProperty {
			continue
		}

		adrField := reflect.ValueOf(new).Elem().Field(i)
		switch typeOfC.Field(i).Type.Kind() {
		case reflect.String:
			adrField.SetString(v.Field(i).String())
		case reflect.Int:
			adrField.SetInt(v.Field(i).Int())
		case reflect.Bool:
			adrField.SetBool(v.Field(i).Bool())
		}
	}

	new.sources = make(map[string]string)
	for key, val := range c.sources {
		new.sources[key] = val
	}

	return new
}

// Source returns where the value of a Config field was defined.
func (c Config) Source(field string) string {
	if c.sources == nil {
		return ConfigSourceDefault
	}
	source, set := c.sources[field]
	if !set {
		return ConfigSourceDefault
	}
	return source
}

func (c Config) String() string {
	v := reflect.ValueOf(c)
	typeOfC := v.Type()

	tableString := &strings.Builder{}
	table := tablewriter.NewWriter(tableString)
	table.SetHeader([]string{"Config", "Value", "Source"})
	table.SetAlignment(tablewriter.ALIGN_LEFT)

	for i := 0; i < v.NumField(); i++ {
		property := typeOfC.Field(i).Name
		if property == sourcesProperty {
			continue
		}

		source := c.sources[property]
		if source == "" {
			source = ConfigSourceDefault
		}

		table.Append([]string{property, fmt.Sprintf("%v", v.Field(i).Interface()), source})
	}

	table.Render()
	return tableString.String()
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
	// we don't os.Setenv("CONFIGOR_ENV", deployment) to stop configor loading
	// files we before we want it to
	err = os.Setenv("CONFIGOR_ENV_PREFIX", "WR")
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// because we want to know the source of every value, we can't take
	// advantage of configor.Load() being able to take all env vars and config
	// files at once. We do it repeatedly and merge results instead
	config := &Config{}
	if cerr := defaults.Set(config); cerr != nil {
		logger.Error(cerr.Error())
		os.Exit(1)
	}

	// load env vars. ManagerUmask is likely to be zero prefixed by user, but
	// that is not converted to int correctly, so fix first
	umask := os.Getenv("WR_MANAGERUMASK")
	if umask != "" && strings.HasPrefix(umask, "0") {
		umask = strings.TrimLeft(umask, "0")
		os.Setenv("WR_MANAGERUMASK", umask)
	}
	configEnv := &Config{}
	err = configor.Load(configEnv)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	config.merge(configEnv, ConfigSourceEnvVar)

	// read each config file and merge results
	configDeploymentBasename := ".wr_config." + deployment + ".yml"

	if configDir := os.Getenv("WR_CONFIG_DIR"); configDir != "" {
		configLoadFromFile(config, filepath.Join(configDir, configCommonBasename), logger)
		configLoadFromFile(config, filepath.Join(configDir, configDeploymentBasename), logger)
	}

	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		logger.Error("could not find home dir", "err", herr)
		os.Exit(1)
	}
	configLoadFromFile(config, filepath.Join(home, configCommonBasename), logger)
	configLoadFromFile(config, filepath.Join(home, configDeploymentBasename), logger)

	configLoadFromFile(config, filepath.Join(pwd, configCommonBasename), logger)
	configLoadFromFile(config, filepath.Join(pwd, configDeploymentBasename), logger)

	// adjust properties as needed
	config.Deployment = deployment

	// convert the possible ~/ in Manager_dir to abs path to user's home
	config.ManagerDir = TildaToHome(config.ManagerDir)
	config.ManagerDir += "_" + deployment

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
		config.ManagerPort = calculatePort(config, "cli", logger)
	}
	if config.ManagerWeb == "" {
		config.ManagerWeb = calculatePort(config, "webi", logger)
	}

	return *config
}

func configLoadFromFile(config *Config, path string, logger log15.Logger) {
	_, err := os.Stat(path)
	if err != nil {
		return
	}

	configFile := config.clone()
	err = configor.Load(configFile, path)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	config.merge(configFile, path)
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
func calculatePort(config *Config, ptype string, logger log15.Logger) (port string) {
	uid, err := Userid()
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	// our port must be greater than 1024, and by basing on user id we can
	// avoid conflicts with other users of wr on the same machine; we
	// multiply by 4 because we have to reserve 4 ports for each user
	pn := portsMinPort + (uid * portsNeeded)

	found := false
	if pn+portsNeeded-1 > portsMaxPort {
		pn = findPorts(logger)
		found = true
	}

	if config.Deployment == Development {
		pn += portsProdDevDiff
	}
	if ptype == "webi" {
		pn += portsCmdWebDiff
	} else if found {
		config.ManagerWeb = strconv.Itoa(pn + portsCmdWebDiff)
	}

	// it's easier for the things that use this port number if it's a string
	// (because it's used as part of a connection string)
	return strconv.Itoa(pn)
}

// findPorts asks the OS for an available port range, then asks the user if
// they'd like to use it and writes it to their config file.
func findPorts(logger log15.Logger) int {
	checker, err := port.NewChecker("localhost")
	if err != nil {
		exitDueToNoPorts(err, "localhost couldn't be connected to", logger)
	}

	min, max, err := checker.AvailableRange(portsNeeded)
	if err != nil {
		exitDueToNoPorts(err, "available localhost ports couldn't be checked", logger)
	}

	fmt.Printf("The default ports couldn't be used for you, but ports %d..%d are available right now.\nYou could use these for your managerport and managerweb config options in production and development.\n", min, max)
	prompt := promptui.Select{
		Label:    "Write them to your config files at ~/.wr_config.production.yml and ~/.wr_config.development.yml?",
		Items:    []string{"Yes", "No"},
		HideHelp: true,
	}

	_, result, err := prompt.Run()
	if err != nil {
		exitDueToNoPorts(err, "didn't understand your response", logger)
	}

	if result == "No" {
		exitDueToNoPorts(nil, "you chose not to use suggested ports", logger)
	}

	writePortsToConfigFiles(min, logger)

	return min
}

// exitDueToNoPorts exits non-zero with an error because we can't continue
// without knowing our ports.
func exitDueToNoPorts(err error, msg string, logger log15.Logger) {
	logger.Error("The default ports couldn't be used for you; please manually set your manager_port and manager_web config options.", "msg", msg, "err", err)
	os.Exit(1)
}

func writePortsToConfigFiles(pn int, logger log15.Logger) {
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		logger.Error("could not find home dir", "err", herr)
		os.Exit(1)
	}

	writePortsToConfigFile(pn, home, Production, logger)
	writePortsToConfigFile(pn+portsProdDevDiff, home, Development, logger)
}

func writePortsToConfigFile(pn int, home, deployment string, logger log15.Logger) {
	f, err := os.OpenFile(filepath.Join(home, ".wr_config."+deployment+".yml"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Error("could not open config file.", "err", err)
		os.Exit(1)
	}

	if _, err := f.WriteString(fmt.Sprintf("managerport: \"%d\"\nmanagerweb: \"%d\"\n", pn, pn+portsCmdWebDiff)); err != nil {
		logger.Error("could not write to config file.", "err", err)
		f.Close()
		os.Exit(1)
	}

	f.Close()
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
