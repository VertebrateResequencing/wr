/* Utility Functions
 * Helper functions for the WR status page.
 */

/**
 * Removes a bad server from the badservers array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} id - The ID of the server to remove
 */
export function removeBadServer(viewModel, id) {
    viewModel.badservers.remove(function (server) {
        return server.ID == id;
    });
}

/**
 * Removes a message from the messages array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} msg - The message to remove
 */
export function removeMessage(viewModel, msg) {
    viewModel.messages.remove(function (schedIssue) {
        return schedIssue.Msg == msg;
    });
}

/**
 * Gets a URL parameter by name
 * @param {string} name - The name of the parameter
 * @returns {string} The value of the parameter
 */
export function getParameterByName(name) {
    var url = window.location.href;
    name = name.replace(/[\[\]]/g, "\\$&");
    var regex = new RegExp("[?&]" + name + "(=([^&#]*)|&|#|$)"),
        results = regex.exec(url);
    if (!results) return null;
    if (!results[2]) return '';
    return decodeURIComponent(results[2].replace(/\+/g, " "));
}

/**
 * Rounds percentages to add up to exactly 100
 * @param {Array} floats - Array of floats that add up to ~100
 * @param {number} min - Minimum percentage value for non-zero entries
 * @returns {Array} Array of integers that add up to exactly 100
 */
export function percentRounder(floats, min) {
    var cumul = 0;
    var baseline = 0;
    var increased = 0;
    var ints = [];
    for (var i = 0; i < floats.length; i++) {
        cumul += floats[i];
        var cumulRounded = Math.round(cumul);
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
}

/**
 * Scales percentages to a given maximum value
 * @param {Array} ints - Array of values to scale
 * @param {number} max - Maximum value to scale to
 * @returns {Array} Scaled values
 */
export function percentScaler(ints, max) {
    var floats = [];
    for (var i = 0; i < ints.length; i++) {
        var unscaled = ints[i];
        var scaled = (unscaled / 100) * max;
        floats.push(scaled);
    }
    return floats;
}

/**
 * Setup Number prototype extensions
 */
export function initNumberPrototypes() {
    // Convert seconds to human-readable duration
    Number.prototype.toDuration = function () {
        var d = this;
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

    // Convert Unix timestamp to date string
    Number.prototype.toDate = function () {
        var d = this;
        var date = new Date(d * 1000);
        var hour = date.getHours() < 10 ? '0' + date.getHours() : date.getHours();
        var min = date.getMinutes() < 10 ? '0' + date.getMinutes() : date.getMinutes();
        var sec = date.getSeconds() < 10 ? '0' + date.getSeconds() : date.getSeconds();
        return date.getFullYear().toString().substr(-2) + "/" + (date.getMonth() + 1) + "/" + date.getDate() + " " + hour + ":" + min + ":" + sec;
    };

    // Convert MB to appropriate unit (MB, GB, TB)
    Number.prototype.mbIEC = function () {
        var size = this * 1048576;
        var i = Math.floor(Math.log(size) / Math.log(1024));
        return (size / Math.pow(1024, i)).toFixed(2) * 1 + ' ' + ['B', 'kB', 'MB', 'GB', 'TB'][i];
    };
}

/**
 * Setup String prototype extensions
 */
export function initStringPrototypes() {
    // Capitalize the first letter of a string
    String.prototype.capitalizeFirstLetter = function () {
        return this.charAt(0).toUpperCase() + this.slice(1);
    };
}

/**
 * Setup Knockout custom bindings
 */
export function initKnockoutBindings() {
    // Bootstrap tooltip binding for Knockout
    ko.bindingHandlers.tooltip = {
        init: function (element, valueAccessor) {
            var local = ko.utils.unwrapObservable(valueAccessor()),
                options = {};

            ko.utils.extend(options, ko.bindingHandlers.tooltip.options);
            ko.utils.extend(options, local);

            $(element).tooltip(options);

            ko.utils.domNodeDisposal.addDisposeCallback(element, function () {
                $(element).tooltip("destroy");
            });
        },
        options: {
            placement: "top",
            trigger: "hover"
        }
    };
}
