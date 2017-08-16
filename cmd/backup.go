// Copyright © 2017 Genome Research Limited
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

package cmd

import (
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/spf13/cobra"
	"time"
)

// options for this cmd
var backupPath string

// backupCmd represents the backup command
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup wr's database",
	Long: `Manually backup wr's job database.

wr automatically backs up its database to the configured location every time
there is a change.

You can use this command to create an additional backup to a different location.
Note that the manager must be running.

(When the manager is stopped, you can backup the database by simply copying it
somewhere.)`,
	Run: func(cmd *cobra.Command, args []string) {
		if backupPath == "" {
			die("--path is required")
		}
		timeout := time.Duration(timeoutint) * time.Second

		jq, err := jobqueue.Connect(addr, "cmds", timeout)
		if err != nil {
			die("%s", err)
		}
		defer jq.Disconnect()

		err = jq.BackupDB(backupPath)
		if err != nil {
			die("%s", err)
		}
	},
}

func init() {
	RootCmd.AddCommand(backupCmd)

	// flags specific to this sub-command
	backupCmd.Flags().StringVarP(&backupPath, "path", "p", "", "backup file path")

	backupCmd.Flags().IntVar(&timeoutint, "timeout", 30, "how long (seconds) to wait to get a reply from 'wr manager'")
}
