// given an array of floats that add up to ~100, returns a corresponding array
// of ints where each of the floats are rounded to a whole number, such that the
// total will be exactly 100. If min > 0, then any input greater than 0 but with
// a rounded result less than min is instead increased to min, and those larger
// than min are correspondingly reduced (this may break if there are too many
// below min).
var percentRounder = function(floats, min) {
    var cumul = 0;
    var baseline = 0;
    var increased = 0;
    var ints = [];
    for (var i = 0; i < floats.length; i++) {
        cumul += floats[i];
        cumulRounded = Math.round(cumul);
        var int = cumulRounded - baseline;
        if (min > 0 && floats[i] > 0 && int < min) {
            increased += (min - int);
            int = min;
        }
        ints.push(int);
        baseline = cumulRounded;
    }
    
    if (increased > 0) {
        var over = [];
        var totalOver = 0;
        for (var i = 0; i < ints.length; i++) {
            if (ints[i] > min) {
                over.push(i);
                totalOver += ints[i];
            }
        }
        
        var decreased = 0;
        for (var i = 0; i < over.length; i++) {
            var intIndex = over[i];
            var intVal = ints[intIndex];
            var proportion = intVal / totalOver;
            var decrease = Math.ceil(proportion * increased);
            if (decreased + decrease > increased) {
                decrease = increased - decreased;
            }
            ints[intIndex] = intVal - decrease;
            decreased += decrease;
        }
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