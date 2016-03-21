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
string is supplied, deployment is taken from the environment variable
VRPIPE_DEPLOYMENT. Otherwise it defaults to development.

Multiple of these files can be used to have settings that are common to
multiple users and deployments, and settings specific to users or deployments.

Settings found in no file can be set with the environment variable
VRPIPE_<setting name in caps>, eg.
export VRPIPE_REDIS="127.0.0.1:6379"
*/
func ConfigLoad(deployment string) Config {
    if deployment != "development" && deployment != "production" {
        deployment = "development"
        if deploymentEnv := os.Getenv("VRPIPE_DEPLOYMENT"); deploymentEnv != "" {
            if (deploymentEnv == "production") {
                deployment = "production"
            }
        }
    }
    fmt.Printf("deployment: %s\n", deployment)
    os.Setenv("CONFIGOR_ENV", deployment)
    os.Setenv("CONFIGOR_ENV_PREFIX", "VRPIPE")
    ConfigDeploymentBasename := ".vrpipe_config." + deployment + ".yml"
    
    // read the config files. We have to check file existence before passing
    // these to configor.Load, or it will complain
    var configFiles []string
    pwd, err := os.Getwd()
    if err != nil {
        fmt.Println(err)
        os.Exit(1)
    }
    configFile := filepath.Join(pwd, ConfigCommonBasename)
    _, err = os.Stat(configFile)
    if _, err2 := os.Stat(filepath.Join(pwd, ConfigDeploymentBasename)); err == nil || err2 == nil {
        fmt.Println("local ok\n")
        configFiles = append(configFiles, configFile)
    }
    if home := os.Getenv("HOME"); home != "" {
        configFile = filepath.Join(home, ConfigCommonBasename)
        _, err = os.Stat(configFile)
        if _, err2 := os.Stat(filepath.Join(home, ConfigDeploymentBasename)); err == nil || err2 == nil {
            fmt.Println("home ok\n")
            configFiles = append(configFiles, configFile)
        }
    }
    if configDir := os.Getenv("VRPIPE_CONFIG_DIR"); configDir != "" {
        configFile = filepath.Join(configDir, ConfigCommonBasename)
        _, err = os.Stat(configFile)
        if _, err2 := os.Stat(filepath.Join(configDir, ConfigDeploymentBasename)); err == nil || err2 == nil {
            fmt.Println("env ok\n")
            configFiles = append(configFiles, configFile)
        }
    }
    fmt.Printf("config: %#v\n", configFiles)
    
    config := Config{}
    configor.Load(&config, configFiles...)
    
    return config
}

