// Copyright © 2018 Genome Research Limited
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

package internal

// this file has general utility functions

import "math"

const floatFixedPrecision float64 = 3

// round rounds a float64.
func round(num float64) int {
	// taken from https://stackoverflow.com/a/29786394
	return int(num + math.Copysign(0.5, num))
}

// toFixed rounds a float64 to 3 decimal places.
func toFixed(num float64) float64 {
	// taken from https://stackoverflow.com/a/29786394
	output := math.Pow(10, floatFixedPrecision)
	return float64(round(num*output)) / output
}

// FloatLessThan tells you if a < b, treating both float64s rounded to 3 decimal
// places of precision.
func FloatLessThan(a, b float64) bool {
	return toFixed(a) < toFixed(b)
}

// FloatSubtract does a - b, treating both float64s rounded to 3 decimal places
// of precision.
func FloatSubtract(a, b float64) float64 {
	return toFixed(toFixed(a) - toFixed(b))
}

// FloatAdd does a + b, treating both float64s rounded to 3 decimal places of
// precision.
func FloatAdd(a, b float64) float64 {
	return toFixed(toFixed(a) + toFixed(b))
}
