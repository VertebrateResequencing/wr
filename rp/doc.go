/*******************************************************************************
 * Copyright (c) 2017, 2024 Genome Research Ltd.
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

/*
Package rp ("resource protector") provides functions that help control access to
some limited resource.

You first create a Protector, and then make Request()s for tokens. Requests give
you Receipts that you use to WaitUntilGranted() to know when a request succeeded
and you "have" the tokens. You can now use whatever the actual resource is. Once
you're done with it you Release() the request so that some other request can
"use" those tokens.

The Protector offers these guarantees:

	# The maximum number of requests that are granted and in play at any one time
	  is the lesser of the Protector's maxSimultaneous value or the return value
	  of the Protector's AvailabilityCallback (if set).
	# Requests (and the calling of the AvailabilityCallback, if set) are granted
	  with at least a delay of the Protector's delayBetween value between each
	  grant (or call).
	# If clients fail to release granted requests, they will be automatically
	  released.

	  import "github.com/VertebrateResequencing/wr/rp"

	  p := rp.New("irods", 2 * time.Second, 20, 5 * time.Minute)

	  // now every time you want use the protected resource, make a request:
	  receipt, err := p.Request(1)
	  p.WaitUntilGranted(receipt)

	  // now use the irods resource; if using it will take longer than 5mins,
	  // arrange to call p.Touch(receipt) every, say, 2.5mins until you're done

	  // once you've finished using the resource:
	  p.Release(receipt)
*/
package rp
