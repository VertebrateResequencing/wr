/* WebSocket Handler
 * Handles WebSocket communication for the WR status page
 */
import { removeBadServer, setupLiveWalltime } from '/js/wr/utility.js';
import { createRepGroupTracker } from '/js/wr/inflight-tracking.js';

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
    return detailsArray().some(job => job.Key === key);
}

/**
 * Handles job details messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleJobDetailsMessage(viewModel, json) {
    var rg = json['RepGroup'];

    // Handle search mode - add to search results instead of details
    if (viewModel.isSearchMode()) {
        // Skip push updates in search results
        if (json['IsPushUpdate']) {
            return;
        }

        // Add to search results array
        viewModel.searchResults.push(json);
        return;
    }

    if (viewModel.detailsOA && rg == viewModel.detailsRepgroup) {
        // Get the current rep group object 
        const repgroupId = viewModel.detailsRepgroup;
        let repgroup = null;

        // Find the repgroup in the array
        for (let i = 0; i < viewModel.repGroups.length; i++) {
            if (viewModel.repGroups[i].id === repgroupId) {
                repgroup = viewModel.repGroups[i];
                break;
            }
        }

        // Check if we're using a custom state filter for this repgroup
        if (repgroup && repgroup.hasCustomFilter && repgroup.selectedFilter
            && repgroup.selectedFilter !== 'total') {

            // If the incoming job doesn't match our filter, skip it
            if (json.State !== repgroup.selectedFilter) {
                return;
            }
        }

        // Check if this is a push update for an existing job
        if (json['IsPushUpdate']) {
            const jobs = viewModel.detailsOA();
            for (const job of jobs) {
                if (job.Key === json.Key) {
                    // Set up LiveWalltime for the job
                    setupLiveWalltime(json, json['Walltime'], viewModel);

                    // Simply replace the job at the same index
                    const index = jobs.indexOf(job);
                    viewModel.detailsOA.splice(index, 1, json);
                    return;
                }
            }
            // If we get here, this is a push update for a job we don't have - ignore it
            return;
        }

        // Skip if this job already exists in the details array (non-push updates)
        if (jobExists(viewModel.detailsOA, json['Key'])) {
            return;
        }

        // Set up LiveWalltime for the job
        setupLiveWalltime(json, json['Walltime'], viewModel);

        // Create a key for this exitcode+reason combination
        const exitReasonKey = `${json.Exitcode}:${json.FailReason || ''}`;

        // Add TotalSimilar to this job if it's part of a tracked batch
        if (viewModel.newJobsInfo[exitReasonKey]) {
            json.TotalSimilar = viewModel.newJobsInfo[exitReasonKey].totalSimilar;
        }

        // Add the job to the details array
        viewModel.detailsOA.push(json);

        // If this job matches a batch we're tracking, update the count and divider text
        if (viewModel.newJobsInfo[exitReasonKey] && viewModel.newJobsInfo[exitReasonKey].dividerElement) {
            const batchInfo = viewModel.newJobsInfo[exitReasonKey];

            // Increment the count for this batch
            batchInfo.batchCount = (batchInfo.batchCount || 0) + 1;

            // Update the divider text with the current count - do this EVERY time
            batchInfo.dividerElement.innerHTML = `<span class="jobs-divider-label">
                  ${batchInfo.batchCount} more jobs${batchInfo.exitCode < 1 ? '' :
                    ` that exited ${batchInfo.exitCode}${batchInfo.failReason ? ` because "${batchInfo.failReason}"` : ''}`}
                 </span>`;
        }
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

    for (const si of messages) {
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
