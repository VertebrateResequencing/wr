/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>, Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package container

// this file has functions for dealing with containers.

import (
	"context"
	"strings"
)

// Container struct represents a container type with specific properties.
type Container struct {
	ID     string
	Names  []string
	client Interactor
}

// Stats struct represents a container stats type with memory and cpu
// properties.
type Stats struct {
	MemoryMB int
	CPUSec   int
}

// Stats returns the current memory usage (RSS, in MB) and total CPU usage (in
// seconds) of this container.
func (c *Container) Stats(ctx context.Context) (*Stats, error) {
	stats, err := c.client.ContainerStats(ctx, c.ID)
	if err != nil {
		return nil, err
	}

	return stats, nil
}

// TrimNamePrefixes trims the '/' from this container's names. You must call
// this after adding names that might contain the prefix.
func (c *Container) TrimNamePrefixes() {
	cnames := make([]string, len(c.Names))

	for idx, name := range c.Names {
		tName := strings.TrimPrefix(name, "/")
		cnames[idx] = tName
	}

	c.Names = cnames
}

// HasName checks if a given name is one of this container's names. Make sure to
// call the TrimNamePrefixes on the container before calling this function.
func (c *Container) HasName(name string) bool {
	for _, cname := range c.Names {
		if cname == name {
			return true
		}
	}

	return false
}
