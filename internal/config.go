// Package internal houses code for vrpipe's general utility functions
package internal

import (
    "fmt"
    "os"
    "path/filepath"
    "github.com/jinzhu/configor"
)

const (
    ConfigCommonBasename = ".vrpipe_config.yml"
)

type Config struct {
    Redis string `default:"127.0.0.1:6379"`
    Beanstalk string `default:"127.0.0.1:11300"`
}

/*
ConfigLoad loads configuration settings from files and environment
variables.

We prefer settings in config file in current dir over config file in home
directory over config file in dir pointed to by VRPIPE_CONFIG_DIR.

The deployment argument determines if we read .vrpipe_config.production.yml or
.vrpipe_config.development.yml; we always read .vrpipe_config.yml. If the empty
string is supplied, deployment is development if you're in the git repository
directory. Otherwise, deployment is taken from the environment variable
VRPIPE_DEPLOYMENT, and if that's not set it defaults to production.

Multiple of these files can be used to have settings that are common to
multiple users and deployments, and settings specific to users or deployments.

Settings found in no file can be set with the environment variable
VRPIPE_<setting name in caps>, eg.
export VRPIPE_REDIS="127.0.0.1:6379"
*/
func ConfigLoad(deployment string) Config {
    pwd, err := os.Getwd()
    if err != nil {
        fmt.Println(err)
        os.Exit(1)
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
                if (deploymentEnv == "development") {
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

