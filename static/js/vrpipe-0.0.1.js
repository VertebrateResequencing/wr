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

// Number(1234).toDuration() returns a string converting the seconds supplied
// to a human readable format from days to milliseconds, skipping unnecessary
// parts
Number.prototype.toDuration = function () {
    d = this;
    var days = Math.floor(d / 86400);
    var h = Math.floor(d % 86400 / 3600);
    var m = Math.floor(d % 3600 / 60);
    var s = Math.floor(d % 3600 % 60);
    
    var dur = '';
    if (days > 0) {
        dur += days + 'd ';
    }
    if (h > 0) {
        dur += h + 'h ';
    }
    if (m > 0) {
        dur += m + 'm ';
    }
    if (s > 0) {
        dur += s + 's ';
    }
    if (dur == '') {
        var ms = Math.round(((d % 3600 % 60) - s) * 1000);
        dur += ms + 'ms';
    }
    return dur;
}