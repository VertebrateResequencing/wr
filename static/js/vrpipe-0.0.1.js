// given an array of floats that add up to ~100, returns a corresponding array
// of ints where each of the floats are rounded to a whole number, such that the
// total will be exactly 100
var percentRounder = function(floats) {
    var cumul = 0;
    var baseline = 0;
    var ints = [];
    for (var i = 0; i < floats.length; i++) {
        cumul += floats[i];
        cumulRounded = Math.round(cumul);
        ints.push(cumulRounded - baseline);
        baseline = cumulRounded;
    }
    return ints;
};