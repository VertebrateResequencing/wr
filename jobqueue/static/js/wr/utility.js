/* Utility Functions
 * Helper functions for the WR status page.
 */

/**
 * Removes a bad server from the badservers array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} id - The ID of the server to remove
 */
export function removeBadServer(viewModel, id) {
    viewModel.badservers.remove(server => server.ID === id);
}

/**
 * Removes a message from the messages array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} msg - The message to remove
 */
export function removeMessage(viewModel, msg) {
    viewModel.messages.remove(schedIssue => schedIssue.Msg === msg);
}

/**
 * Gets a URL parameter by name
 * @param {string} name - The name of the parameter
 * @returns {string|null} The value of the parameter or null if not found
 */
export function getParameterByName(name) {
    const url = window.location.href;
    const sanitizedName = name.replace(/[\[\]]/g, "\\$&");
    const regex = new RegExp(`[?&]${sanitizedName}(=([^&#]*)|&|#|$)`);
    const results = regex.exec(url);

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
    let cumul = 0;
    let baseline = 0;
    let increased = 0;
    const ints = [];

    // First pass: round numbers and identify values below minimum
    for (let i = 0; i < floats.length; i++) {
        cumul += floats[i];
        const cumulRounded = Math.round(cumul);
        let int = cumulRounded - baseline;

        if (min > 0 && floats[i] > 0 && int < min) {
            increased += (min - int);
            int = min;
        }

        ints.push(int);
        baseline = cumulRounded;
    }

    // Only proceed with adjustments if we increased some values
    if (increased > 0) {
        const over = [];
        let totalOver = 0;

        // Identify values that are above minimum
        for (let i = 0; i < ints.length; i++) {
            if (ints[i] > min) {
                over.push(i);
                totalOver += ints[i];
            }
        }

        // Decrease values proportionally to maintain total
        let decreased = 0;
        for (let i = 0; i < over.length; i++) {
            const intIndex = over[i];
            const intVal = ints[intIndex];
            const proportion = intVal / totalOver;
            let decrease = Math.ceil(proportion * increased);

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
    return ints.map(unscaled => (unscaled / 100) * max);
}

/**
 * Convert seconds to human-readable duration
 * @param {number} seconds - The seconds to format
 * @returns {string} Formatted duration string
 */
export function toDuration(seconds) {
    var d = seconds;
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

/**
 * Convert Unix timestamp to date string
 * @param {number} timestamp - Unix timestamp in seconds
 * @returns {string} Formatted date string
 */
export function toDate(timestamp) {
    var date = new Date(timestamp * 1000);
    var hour = date.getHours() < 10 ? '0' + date.getHours() : date.getHours();
    var min = date.getMinutes() < 10 ? '0' + date.getMinutes() : date.getMinutes();
    var sec = date.getSeconds() < 10 ? '0' + date.getSeconds() : date.getSeconds();
    return date.getFullYear().toString().substr(-2) + "/" + (date.getMonth() + 1) + "/" + date.getDate() + " " + hour + ":" + min + ":" + sec;
}

/**
 * Convert MB to appropriate unit (MB, GB, TB)
 * @param {number} mbSize - Size in MB
 * @returns {string} Formatted size string with appropriate unit
 */
export function mbIEC(mbSize) {
    var size = mbSize * 1048576;
    var i = Math.floor(Math.log(size) / Math.log(1024));
    return (size / Math.pow(1024, i)).toFixed(2) * 1 + ' ' + ['B', 'kB', 'MB', 'GB', 'TB'][i];
}

/**
 * Capitalize the first letter of a string
 * @param {string} str - The string to capitalize
 * @returns {string} String with capitalized first letter
 */
export function capitalizeFirstLetter(str) {
    return str.charAt(0).toUpperCase() + str.slice(1);
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

/**
 * Sets up a LiveWalltime computed observable for a job
 * @param {object} job - The job object to set up LiveWalltime for
 * @param {number} walltime - The walltime value to use
 * @param {StatusViewModel} viewModel - The view model containing wallTimeUpdaters
 */
export function setupLiveWalltime(job, walltime, viewModel) {
    if (job.State === "running") {
        // Auto-increment walltime for running jobs
        const began = new Date();
        const now = ko.observable(new Date());

        job.LiveWalltime = ko.computed(function () {
            return walltime + ((now() - began) / 1000);
        });

        viewModel.wallTimeUpdaters.push(now);

        // Set up the interval to update the time if not already done
        if (!viewModel.wallTimeUpdater) {
            viewModel.wallTimeUpdater = window.setInterval(function () {
                for (const updater of viewModel.wallTimeUpdaters) {
                    updater(new Date());
                }
            }, 1000);
        }
    } else {
        // Static walltime for non-running jobs
        job.LiveWalltime = ko.computed(function () {
            return walltime;
        });
    }
}
