// Copyright © 2016-2018 Genome Research Limited
// Author: Theo Barber-Bany <tb15@sanger.ac.uk>.
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

package scheduler

import (
	"github.com/inconshreveable/log15"
)

//Kubernetes is the implementer of scheduleri. Modeled after the opst implementation
//in openstack.go
type k8s struct {
	local
	log15.Logger
}

func (s *k8s) schedule(cmd string, req *Requirements, count int) error {
	return nil
}

//Delete the namespace when all pods have exited.
func (s *k8s) cleanup() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.cleaned = true
	err := s.queue.Destroy()
	if err != nil {
		s.Warn("cleanup queue destruction failed", "err", err)
	}

	//TODO: Client-go code to delete namespace.
	//TODO: Do more elegantly later.

	return
}

//Always assume the cluster can schedule the job.
//ToDo: Check if any given node has the required ram / cpu
func (s *k8s) reqCheck(req *Requirements) error {
	return nil
}

//Arbitrarily limit max jobs to 50 for now
//ToDo: Use input from kubectl describe nodes under allocatable
func (s *k8s) canCount(req *Requirements) int {
	return 50
}

//Should create a new pod here. Unsure of how to structure
//Want to use implementation from local, just on a pod running the runner.
func (s *k8s) runCmd(cmd string, req *Requirements, reservedCh chan bool) error {
	return nil
}
