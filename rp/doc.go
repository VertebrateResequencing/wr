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
