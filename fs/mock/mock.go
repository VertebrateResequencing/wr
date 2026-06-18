/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

// package mock contains a mock implementation of VolumeUsageCalculator.
package mock

import "context"

// VolumeUsageCalculator represents a mock implementation of
// fs.VolumeUsageCalculator.
type VolumeUsageCalculator struct {
	SizeFn      func(volumePath string) uint64
	SizeInvoked int
	FreeFn      func(volumePath string) uint64
	FreeInvoked int
}

// Size returns the size of the volume in bytes.
func (v *VolumeUsageCalculator) Size(ctx context.Context, volumePath string) uint64 {
	v.SizeInvoked++

	return v.SizeFn(volumePath)
}

func (v *VolumeUsageCalculator) Free(ctx context.Context, volumePath string) uint64 {
	v.FreeInvoked++

	return v.FreeFn(volumePath)
}
