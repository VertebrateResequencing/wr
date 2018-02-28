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
Package internal houses code for wr's general utility functions.

It also implements the the config system used by the cmd package (see
config.go).

    import "github.com/VertebrateResequencing/wr/internal"
    import "github.com/inconshreveable/log15"
    deployment := internal.DefaultDeployment()
    logger := log15.New()
    logger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))
    config := internal.ConfigLoad(deployment, false, logger)
    port := config.ManagerPort
*/
package internal
