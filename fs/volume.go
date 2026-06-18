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

// package fs is for interacting with file systems.
package fs

import (
	"context"
	"errors"

	"github.com/VertebrateResequencing/wr/backoff"
	"github.com/VertebrateResequencing/wr/retry"
)

const gb uint64 = 1.07374182e9 // for byte to GB conversion
const mb100 uint64 = 104857600 // 100MB in bytes

var errZeroBytes = errors.New("zero bytes claimed")

// VolumeUsageCalculator has methods that provide volume usage information.
type VolumeUsageCalculator interface {
	// Size returns the size of the volume in bytes.
	Size(ctx context.Context, volumePath string) uint64

	// Free returns the free space of the volume in bytes.
	Free(ctx context.Context, volumePath string) uint64
}

// Volume respresents a file system volume.
type Volume struct {
	// Dir is a directory path mounted on the volume of interest. "." is taken
	// to mean the current directory.
	Dir string

	// UsageCalculator is an implementation of VolumeUsageCalculator.
	UsageCalculator VolumeUsageCalculator
}

// Size returns the size of the volume in GB.
func (v *Volume) Size(ctx context.Context) int {
	return int(v.UsageCalculator.Size(ctx, v.Dir) / gb)
}

// NoSpaceLeft tells you if the volume has no more space left (or is within
// 100MB of being full).
func (v *Volume) NoSpaceLeft(ctx context.Context) bool {
	return v.UsageCalculator.Free(ctx, v.Dir) < mb100
}

// CachedVolumeUsageCalculator wraps a VolumeUsageCalculator to provide an
// in-memory cache for the Size() method.
type CachedVolumeUsageCalculator struct {
	UsageCalculator VolumeUsageCalculator
	size            uint64
}

// Size returns the size of the volume in bytes.
func (v *CachedVolumeUsageCalculator) Size(ctx context.Context, volumePath string) uint64 {
	if v.size > 0 {
		return v.size
	}

	v.size = v.UsageCalculator.Size(ctx, volumePath)

	return v.size
}

// Free returns the free space of the volume in bytes.
func (v *CachedVolumeUsageCalculator) Free(ctx context.Context, volumePath string) uint64 {
	return v.UsageCalculator.Free(ctx, volumePath)
}

// CheckedVolumeUsageCalculator wraps a VolumeUsageCalculator to confirm
// multiple times when free space on the volume is reported as 0, before
// returning that answer.
type CheckedVolumeUsageCalculator struct {
	// Retries is the number of attempts at getting the free space that should
	// be made, if the answer is 0
	Retries int

	// Backoff determines the time waited in between attempts. It will be
	// Reset() when free space is greater than 0.
	Backoff *backoff.Backoff

	// UsageCalculator is an implementation of VolumeUsageCalculator.
	UsageCalculator VolumeUsageCalculator
}

type volumeUsageCalculationMethod func(context.Context, string) uint64

// Size returns the size of the volume in bytes. If the answer would be
// 0, this is first re-confirmed multiple times before returning.
func (v *CheckedVolumeUsageCalculator) Size(ctx context.Context, volumePath string) uint64 {
	return retryIfZero(ctx, v.Retries, v.Backoff, v.UsageCalculator.Size, volumePath)
}

// Free returns the free space of the volume in bytes. If the answer would be
// 0, this is first re-confirmed multiple times before returning.
func (v *CheckedVolumeUsageCalculator) Free(ctx context.Context, volumePath string) uint64 {
	return retryIfZero(ctx, v.Retries, v.Backoff, v.UsageCalculator.Free, volumePath)
}

// retryIfZero retries the given method up to retries times if the method
// returns zero. If it returns greater than zero, backoff is Reset().
func retryIfZero(ctx context.Context,
	retries int,
	backoff *backoff.Backoff,
	f volumeUsageCalculationMethod,
	arg string) uint64 {
	var bytes uint64
	status := retry.Do(
		ctx,
		operationReturnsErrIfZero(ctx, f, arg, &bytes),
		&retry.Untils{
			&retry.UntilNoError{},
			&retry.UntilLimit{Max: retries},
		},
		backoff,
		"getting volume usage",
	)

	if status.StoppedBecause == retry.BecauseErrorNil {
		backoff.Reset()
	}

	return bytes
}

// operationReturnsErrIfZero creates a retry.Operation that errors if supplied
// method returns zero given supplied arg. The return value is stored in the
// given bytes arg.
func operationReturnsErrIfZero(ctx context.Context,
	f volumeUsageCalculationMethod,
	arg string,
	bytes *uint64) retry.Operation {
	return func() error {
		*bytes = f(ctx, arg)
		if *bytes == 0 {
			return errZeroBytes
		}

		return nil
	}
}
