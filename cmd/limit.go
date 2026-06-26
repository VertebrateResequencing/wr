/*******************************************************************************
 * Copyright (c) 2019, 2021, 2025 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// options for this cmd.
var limitGroup string

// limitCmd represents the remove command.
var limitCmd = &cobra.Command{
	Use:   "limit",
	Short: "Limit how many jobs in a group run at once",
	Long: `Jobs that were added with a specification of one or more limit groups
can be set to only run a limited number simultaneously.

For example, if you added 100 jobs in limit group "a", and had enough hardware
to run all 100, all of them would run simultaneously.

But if you first set the limit to 10, only 10 of those jobs would run at once.

When adding jobs it is convenient to set the limit of each group ("a:10") at the
same time. This limit command can be used to change the limit afterwards.

Passing just a group name to -g will display the current limit of that limit
group. Groups that are not known about will report -1. Limit group names should
not contain commas or colons.

Suffixing the name with :n, where n is an integer, will set the group's limit to
that number.

Setting a limit of 0 stops any more jobs in that group from running. Setting a
limit of -1 makes that group unlimited.

Supplying no options lists all limits that are currently in place.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			die("Did you mean to specify --group?")
		}

		timeout := time.Duration(timeoutint) * time.Second
		jq := connect(timeout)

		var err error
		defer func() {
			err = jq.Disconnect()
			if err != nil {
				warn("Disconnecting from the server failed: %s", err)
			}
		}()

		if limitGroup == "" {
			var limits map[string]int

			limits, err = jq.GetLimitGroups()
			if err != nil {
				die("%s", err.Error())
			}

			keys := make([]string, 0, len(limits))
			for key := range limits {
				keys = append(keys, key)
			}

			sort.Strings(keys)

			for _, key := range keys {
				fmt.Printf("%s: %d\n", key, limits[key])
			}

			return
		}

		limit, err := jq.GetOrSetLimitGroup(limitGroup)
		if err != nil {
			die("%s", err.Error())
		}

		fmt.Printf("%d\n", limit)
	},
}

func init() {
	RootCmd.AddCommand(limitCmd)

	// flags specific to this sub-command
	limitCmd.Flags().StringVarP(&limitGroup, "group", "g", "", "name of the limit group to view, suffixed with :n to set limit")
}
