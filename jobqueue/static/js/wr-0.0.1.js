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

// given an array of ints that are supposed to be percentages, scale them so
// that they are a percent of the given value.
var percentScaler = function(ints, max) {
    var floats = [];
    for (var i = 0; i < ints.length; i++) {
        var unscaled = ints[i];
        var scaled = (unscaled / 100) * max;
        floats.push(scaled);
    }
    return floats;
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
};

// Number(1234).toDate() returns a string converting the seconds (elapsed since
// Unix epoch) supplied to human readable format
Number.prototype.toDate = function () {
    d = this;
    var date = new Date(d*1000);
    var hour = date.getHours() < 10 ? '0' + date.getHours() : date.getHours();
    var min = date.getMinutes() < 10 ? '0' + date.getMinutes() : date.getMinutes();
    var sec = date.getSeconds() < 10 ? '0' + date.getSeconds() : date.getSeconds();
    return date.getFullYear().toString().substr(-2) + "/" + (date.getMonth() + 1) + "/" + date.getDate() + " " + hour + ":" + min + ":" + sec;
};

// Convert MB in to GB or TB if appropriate
// (based on http://stackoverflow.com/a/20732091/675083)
Number.prototype.mbIEC = function () {
    var size = this * 1048576;
    var i = Math.floor( Math.log(size) / Math.log(1024) );
    return ( size / Math.pow(1024, i) ).toFixed(2) * 1 + ' ' + ['B', 'kB', 'MB', 'GB', 'TB'][i];
};

// capitalize the first letter of a string
// (taken from http://stackoverflow.com/a/1026087/675083)
String.prototype.capitalizeFirstLetter = function() {
    return this.charAt(0).toUpperCase() + this.slice(1);
}

// we have a binding handler to make bootstrap tooltips work with knockout
// (taken from https://stackoverflow.com/a/16876013/675083)
ko.bindingHandlers.tooltip = {
    init: function(element, valueAccessor) {
        var local = ko.utils.unwrapObservable(valueAccessor()),
            options = {};

        ko.utils.extend(options, ko.bindingHandlers.tooltip.options);
        ko.utils.extend(options, local);

        $(element).tooltip(options);

        ko.utils.domNodeDisposal.addDisposeCallback(element, function() {
            $(element).tooltip("destroy");
        });
    },
    options: {
        placement: "top",
        trigger: "hover"
    }
};

// read a query parameter, from https://stackoverflow.com/a/901144/675083
var getParameterByName = function(name) {
    var url = window.location.href;
    name = name.replace(/[\[\]]/g, "\\$&");
    var regex = new RegExp("[?&]" + name + "(=([^&#]*)|&|#|$)"),
        results = regex.exec(url);
    if (!results) return null;
    if (!results[2]) return '';
    return decodeURIComponent(results[2].replace(/\+/g, " "));
};
