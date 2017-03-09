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

/*
Package cmd implements wr's command line interface.

It is implemented using cobra, so see github.com/spf13/cobra for details. On top
of cobra we use our own config system; see internal/config.go.

cmd/root.go contains general utility functions for use by any of the sub command
implementations. It also give the help text you see when you run `wr` by itself.

Each wr sub-command (eg. 'add' or 'manager') is implemented in its own .go file.
*/
package cmd
