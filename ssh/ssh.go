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

// Package ssh handles connecting to remote hosts using public-private keys,
// and running commands on that host.
package ssh

import (
	"bytes"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io/ioutil"
	"os/user"
	"strconv"
	"strings"
)

// The code in this package is mostly based on
// http://golang-basic.blogspot.co.uk/2014/06/step-by-step-guide-to-ssh-using-go.html

// getKeyFile gets the user's key file from their .ssh directory
func getKeyFile(usr *user.User, keytype string) (key ssh.Signer, err error) {
	file := usr.HomeDir + "/.ssh/id_" + keytype
	buf, err := ioutil.ReadFile(file)
	if err != nil {
		return
	}
	key, err = ssh.ParsePrivateKey(buf)
	if err != nil {
		return
	}
	return
}

// sshConfig gets the config needed to authenticate when Dial()ing
func sshConfig(keytype string) (config *ssh.ClientConfig, err error) {
	usr, _ := user.Current()

	key, err := getKeyFile(usr, keytype)
	if err != nil {
		err = fmt.Errorf("Failed to get your ssh key file: %s\n", err.Error())
		return
	}

	config = &ssh.ClientConfig{
		User: usr.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(key),
		},
	}
	return
}

// connect connects to a remote host using your private ssh key for
// authentication.
func connect(host string, port int) (client *ssh.Client, err error) {
	keytype := "rsa"
	config, err := sshConfig(keytype)
	if err != nil {
		keytype = "dsa"
		config, err = sshConfig(keytype)
		if err != nil {
			return
		}
		err = nil
	}

	hostAndPort := host + ":" + strconv.Itoa(port)
	client, err = ssh.Dial("tcp", hostAndPort, config)
	if err != nil {
		err = fmt.Errorf("Failed to ssh to %s: %s\n", hostAndPort, err.Error())

		// if it failed with rsa, try again with dsa
		if keytype == "rsa" && strings.Contains(err.Error(), "unable to authenticate") {
			keytype = "dsa"
			config, err2 := sshConfig(keytype)
			if err2 != nil {
				return
			}
			client, err2 = ssh.Dial("tcp", hostAndPort, config)
			if err2 != nil {
				return
			}
			err = nil
		}
	}

	return
}

// RunCmd runs a command on a remote host. If background argument is true, the
// remote command will be backgrounded and will return no response.
func RunCmd(host string, port int, cmd string, background bool) (response string, err error) {
	client, err := connect(host, port)
	if err != nil {
		err = fmt.Errorf("Failed to connect to %s:%d: %s\n", host, port, err.Error())
		return
	}

	session, err := client.NewSession()
	if err != nil {
		err = fmt.Errorf("Failed to create session on %s:%d: %s\n", host, port, err.Error())
		return
	}
	defer session.Close()

	origcmd := cmd
	if background {
		cmd = "sh -c 'nohup " + cmd + " > /dev/null 2>&1 &'"
	}

	var b bytes.Buffer
	session.Stdout = &b
	if err = session.Run(cmd); err != nil {
		err = fmt.Errorf("Failed to run [%s] on %s:%d: %s\n", origcmd, host, port, err.Error())
		return
	}
	response = b.String()
	return
}
