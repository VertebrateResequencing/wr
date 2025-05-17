/* WebSocket Handler
 * Handles WebSocket communication for the WR status page
 */
import { removeBadServer } from '/js/utility.js';
import { createRepGroupTracker } from '/js/inflight-tracking.js';

/**
 * Sets up the WebSocket connection and message handling
 * @param {StatusViewModel} viewModel - The main view model
 */
export function setupWebSocket(viewModel) {
    if (window.WebSocket === undefined) {
        viewModel.statuserror.push("Your browser does not support WebSockets");
        return;
    }

    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${location.hostname}:${location.port}/status_ws?token=${viewModel.token}`;

    try {
        viewModel.ws = new WebSocket(wsUrl);

        viewModel.ws.onopen = () => {
            viewModel.ws.send(JSON.stringify({ Request: "current" }));
        };

        viewModel.ws.onclose = () => {
            viewModel.statuserror.push("Connection to the manager has been lost!");
        };

        viewModel.ws.onerror = (error) => {
            viewModel.statuserror.push(`WebSocket error: ${error.message || 'Unknown error'}`);
        };

        viewModel.ws.onmessage = (e) => {
            try {
                const json = JSON.parse(e.data);

                if (json.hasOwnProperty('FromState')) {
                    handleStateChangeMessage(viewModel, json);
                } else if (json.hasOwnProperty('State')) {
                    handleJobDetailsMessage(viewModel, json);
                } else if (json.hasOwnProperty('IP')) {
                    handleServerMessage(viewModel, json);
                } else if (json.hasOwnProperty('Msg')) {
                    handleSchedulerMessage(viewModel, json);
                }
            } catch (error) {
                console.error("Error processing message:", error);
                viewModel.statuserror.push(`Error processing message: ${error.message}`);
            }
        };
    } catch (error) {
        viewModel.statuserror.push(`Failed to connect: ${error.message}`);
    }
}

/**
 * Handles state change messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleStateChangeMessage(viewModel, json) {
    var rg = json['RepGroup'];
    var repgroup;

    if (rg == "+all+") {
        repgroup = viewModel.inflight;
    } else if (viewModel.repGroupLookup.hasOwnProperty(rg)) {
        repgroup = viewModel.repGroups[viewModel.repGroupLookup[rg]];
    } else {
        // Create a new rep group tracker
        repgroup = createRepGroupTracker(rg, viewModel.rateLimit);

        viewModel.repGroups.push(repgroup);
        viewModel.repGroupLookup[rg] = viewModel.repGroups.length - 1;
        viewModel.sortableRepGroups.push(repgroup);
    }

    var from, to;

    // Determine the 'from' state
    switch (json['FromState']) {
        case 'delayed': from = repgroup['delayed']; break;
        case 'dependent': from = repgroup['dependent']; break;
        case 'ready': from = repgroup['ready']; break;
        case 'running': from = repgroup['running']; break;
        case 'lost': from = repgroup['lost']; break;
        case 'buried': from = repgroup['buried']; break;
    }

    // Check if we should ignore this transition
    if (viewModel.ignore.hasOwnProperty(json['RepGroup']) &&
        viewModel.ignore[json['RepGroup']].hasOwnProperty(json['ToState']) &&
        viewModel.ignore[json['RepGroup']][json['ToState']] >= json['Count']) {

        viewModel.ignore[json['RepGroup']][json['ToState']] -= json['Count'];

        if (viewModel.ignore[json['RepGroup']][json['ToState']] == 0) {
            delete viewModel.ignore[json['RepGroup']][json['ToState']];

            if (Object.keys(viewModel.ignore[json['RepGroup']]).length == 0) {
                delete viewModel.ignore[json['RepGroup']];
            }
        }
    } else {
        // Determine the 'to' state
        switch (json['ToState']) {
            case 'delayed': to = repgroup['delayed']; break;
            case 'dependent': to = repgroup['dependent']; break;
            case 'ready': to = repgroup['ready']; break;
            case 'running': to = repgroup['running']; break;
            case 'lost': to = repgroup['lost']; break;
            case 'buried': to = repgroup['buried']; break;
            case 'complete':
                if (rg != "+all+") {
                    to = repgroup['complete'];
                }
                break;
            case 'deleted':
                if (rg != "+all+") {
                    to = repgroup['deleted'];
                }
                break;
        }
    }

    // Update the counts
    repgroup['delay_compute'] = to ? 1 : 0;

    if (from) {
        var newfrom = from() - json['Count'];

        if (newfrom >= 0) {
            from(newfrom);
        } else {
            // Handle out-of-order transitions
            if (!viewModel.ignore.hasOwnProperty(json['RepGroup'])) {
                viewModel.ignore[json['RepGroup']] = {};
            }

            if (viewModel.ignore[json['RepGroup']].hasOwnProperty(json['FromState'])) {
                viewModel.ignore[json['RepGroup']][json['FromState']] += json['Count'];
            } else {
                viewModel.ignore[json['RepGroup']][json['FromState']] = json['Count'];
            }

            from(0);
        }
    }

    repgroup['delay_compute'] = 0;

    if (to) {
        to(to() + json['Count']);
    }
}

/**
 * Checks if a job with the given key already exists in the details array
 * @param {ObservableArray} detailsArray - The array of job details
 * @param {string} key - The job key to check for
 * @returns {boolean} True if job already exists
 */
function jobExists(detailsArray, key) {
    const jobs = detailsArray();
    for (let i = 0; i < jobs.length; i++) {
        if (jobs[i].Key === key) {
            return true;
        }
    }
    return false;
}

/**
 * Handles job details messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleJobDetailsMessage(viewModel, json) {
    var rg = json['RepGroup'];

    if (viewModel.detailsOA && rg == viewModel.detailsRepgroup) {
        // Skip if this job already exists in the details array
        if (jobExists(viewModel.detailsOA, json['Key'])) {
            return;
        }

        var walltime = json['Walltime'];

        if (json['State'] == "running") {
            // Auto-increment walltime for running jobs
            var began = new Date();
            var now = ko.observable(new Date());

            json['LiveWalltime'] = ko.computed(function () {
                return walltime + ((now() - began) / 1000);
            });

            viewModel.wallTimeUpdaters.push(now);

            if (!viewModel.wallTimeUpdater) {
                viewModel.wallTimeUpdater = window.setInterval(function () {
                    var arrayLength = viewModel.wallTimeUpdaters.length;

                    for (var i = 0; i < arrayLength; i++) {
                        viewModel.wallTimeUpdaters[i](new Date());
                    }
                }, 1000);
            }
        } else {
            json['LiveWalltime'] = ko.computed(function () {
                return walltime;
            });
        }

        viewModel.detailsOA.push(json);
    }
}

/**
 * Handles server messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleServerMessage(viewModel, json) {
    if (json['IsBad']) {
        viewModel.badservers.push(json);
    } else {
        removeBadServer(viewModel, json['ID']);
    }
}

/**
 * Handles scheduler messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleSchedulerMessage(viewModel, json) {
    var updated = false;
    var messages = viewModel.messages();

    for (var i = 0; i < messages.length; ++i) {
        var si = messages[i];

        if (si.Msg == json['Msg']) {
            si.LastDate(json['LastDate']);
            si.Count(json['Count']);
            updated = true;
            break;
        }
    }

    if (!updated) {
        var schedIssue = {
            'Msg': json['Msg'],
            'FirstDate': json['FirstDate'],
            'LastDate': ko.observable(json['LastDate']),
            'Count': ko.observable(json['Count']),
        };

        viewModel.messages.push(schedIssue);
    }
}
