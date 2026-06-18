/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Based on: https://blog.gopheracademy.com/advent-2016/context-logging/
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

package clog

import (
	"context"

	"github.com/inconshreveable/log15"
)

// correlationIDType is for the *Key constants, which provide private quick-to-
// access value storage in the With* functions.
type correlationIDType int

const (
	retrySetKey correlationIDType = iota
	retryActivityKey
	retryNumKey
	contextJobKey
	contextServerID
	contextSchedulerType
	contextCloudType
	contextCallValue
	contextServerFlavor
	contextLogHandler
)

// ContextForRetries returns a context which knows a new unique retryset
// ID, as well as the given retryactivity.
func ContextForRetries(ctx context.Context, activity string) context.Context {
	return context.WithValue(
		context.WithValue(ctx, retrySetKey, UniqueID()),
		retryActivityKey,
		activity,
	)
}

// ContextWithRetryNum returns a context which knows the given retrynum.
func ContextWithRetryNum(ctx context.Context, retrynum int) context.Context {
	return context.WithValue(ctx, retryNumKey, retrynum)
}

// ContextWithJobKey returns a context which knows the given key.
func ContextWithJobKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextJobKey, key)
}

// ContextWithServerID returns a context which knows the given id.
func ContextWithServerID(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextServerID, key)
}

// ContextWithCloudType returns a context which knows the given cloud type.
func ContextWithCloudType(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextCloudType, key)
}

// ContextWithSchedulerType returns a context which knows the given scheduler type.
func ContextWithSchedulerType(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextSchedulerType, key)
}

// ContextWithCallValue returns a context which knows the given call value.
func ContextWithCallValue(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextCallValue, key)
}

// ContextWithServerFlavor returns a context which knows the given server flavour.
func ContextWithServerFlavor(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, contextServerFlavor, key)
}

// ContextWithLogHandler returns a context which knows the given log handler.
func ContextWithLogHandler(ctx context.Context, key log15.Handler) context.Context {
	return context.WithValue(ctx, contextLogHandler, key)
}
