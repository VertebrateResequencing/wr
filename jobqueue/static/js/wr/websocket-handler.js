/* WebSocket Handler
 * Handles WebSocket communication for the WR status page
 */
import { removeBadServer, setupLiveWalltime } from '/js/wr/utility.js';
import { createRepGroupTracker } from '/js/wr/inflight-tracking.js';

const countProperties = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried', 'deleted', 'complete'];
const percentProperties = ['delayPct', 'dependentPct', 'suspendedPct', 'readyPct', 'runPct', 'lostPct', 'buryPct', 'deletePct', 'completePct'];
const unixNanoThreshold = 1000000000000000;
const managerLostWarning = "Connection to the manager has been lost!";
const webSocketErrorPrefix = "WebSocket error:";

function isConnectionStatusWarning(message) {
    return message === managerLostWarning ||
        (typeof message === 'string' && message.startsWith(webSocketErrorPrefix));
}

function clearConnectionStatusWarnings(viewModel) {
    viewModel.statuserror.remove(isConnectionStatusWarning);
}

function resetTrackerCounts(tracker) {
    for (const property of countProperties.concat(percentProperties)) {
        if (tracker && typeof tracker[property] === 'function') {
            tracker[property](0);
        }
    }

    if (tracker) {
        tracker.old_total = 0;
    }
}

function resetLiveCounts(viewModel) {
    resetTrackerCounts(viewModel.inflight);

    for (const repgroup of viewModel.repGroups) {
        if (!repgroup.id.startsWith('search:')) {
            resetTrackerCounts(repgroup);
        }
    }
}

function getOrCreateRepGroupTracker(viewModel, rg) {
    if (viewModel.repGroupLookup.hasOwnProperty(rg)) {
        return viewModel.repGroups[viewModel.repGroupLookup[rg]];
    }

    const repgroup = createRepGroupTracker(rg, viewModel.rateLimit);

    viewModel.repGroups.push(repgroup);
    viewModel.repGroupLookup[rg] = viewModel.repGroups.length - 1;
    viewModel.sortableRepGroups.push(repgroup);

    return repgroup;
}

function normalizeStatusTimestamp(timestamp) {
    if (typeof timestamp !== 'number' || Math.abs(timestamp) < unixNanoThreshold) {
        return timestamp;
    }

    return timestamp / 1000000000;
}

function normalizeJobStatusTimes(job) {
    job.Started = normalizeStatusTimestamp(job.Started);
    job.Ended = normalizeStatusTimestamp(job.Ended);
}

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
    const currentRequest = JSON.stringify({ Request: "current" });
    const reconnectInitialDelay = 1000;
    const reconnectMaxDelay = 30000;
    let reconnectDelay = reconnectInitialDelay;
    let reconnectTimer = null;
    let reportedClose = false;
    const renderedWebSocketErrors = new Set();

    // The server pushes idempotent absolute per-RepGroup state, sending the full
    // current map as soon as we connect (and again on reconnect, since that is a
    // fresh connection). The "current" request only (re)broadcasts the bad-server
    // and scheduler-issue sets; status counts need no resync.
    const sendCurrentStatus = (ws) => {
        if (viewModel.ws === ws && ws.readyState === 1) {
            ws.send(currentRequest);
        }
    };

    const scheduleReconnect = () => {
        if (reconnectTimer !== null) {
            return;
        }

        const delay = reconnectDelay;
        reconnectDelay = Math.min(reconnectDelay * 2, reconnectMaxDelay);

        reconnectTimer = window.setTimeout(() => {
            reconnectTimer = null;
            connect();
        }, delay);
    };

    const connect = () => {
        try {
            const ws = new WebSocket(wsUrl);
            viewModel.ws = ws;

            ws.onopen = () => {
                reconnectDelay = reconnectInitialDelay;
                clearConnectionStatusWarnings(viewModel);
                if (reportedClose) {
                    // A reconnect delivers a fresh full-state push, so clear the
                    // stale live counts before it arrives.
                    resetLiveCounts(viewModel);
                }
                reportedClose = false;
                renderedWebSocketErrors.clear();
                sendCurrentStatus(ws);
            };

            ws.onclose = () => {
                if (viewModel.ws !== ws) {
                    return;
                }

                if (!reportedClose) {
                    viewModel.statuserror.push(managerLostWarning);
                    reportedClose = true;
                }

                scheduleReconnect();
            };

            ws.onerror = (error) => {
                const message = `WebSocket error: ${error.message || 'Unknown error'}`;
                if (!renderedWebSocketErrors.has(message)) {
                    viewModel.statuserror.push(message);
                    renderedWebSocketErrors.add(message);
                }
            };

            ws.onmessage = (e) => {
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
            scheduleReconnect();
        }
    };

    connect();
}

// stateToProperty maps a wire JobState string to the tracker's count observable
// name. States with no bar segment (e.g. "new") map to undefined and are
// ignored.
const stateToProperty = {
    delayed: 'delayed',
    dependent: 'dependent',
    suspended: 'suspended',
    ready: 'ready',
    reserved: 'running',
    running: 'running',
    lost: 'lost',
    buried: 'buried',
    complete: 'complete',
    deleted: 'deleted',
};

/**
 * Handles a v0.36.5-style from->to state-count delta message from the WebSocket
 * by decrementing the FromState count and incrementing the ToState count by
 * Count on the RepGroup tracker (and the "+all+" in-flight tracker). Because the
 * feed is lossy and unordered (v0.36.5 quality), an out-of-order delta that
 * would drive a count negative is instead recorded as an amount to ignore from a
 * future increment of that state, keeping counts non-negative. "+all+" tracks
 * only live states, so terminal complete/deleted are not applied to it.
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data ({ RepGroup, FromState, ToState, Count })
 */
function handleStateChangeMessage(viewModel, json) {
    const rg = json['RepGroup'];
    const isAll = rg == "+all+";
    const tracker = isAll ? viewModel.inflight : getOrCreateRepGroupTracker(viewModel, rg);
    const count = json['Count'];

    if (!viewModel.ignore) {
        viewModel.ignore = {};
    }

    const fromProperty = stateToProperty[json['FromState']];
    const toProperty = terminalOnAll(isAll, json['ToState']) ? undefined : stateToProperty[json['ToState']];

    const ignored = applyIgnoredToState(viewModel, rg, json['ToState'], count);

    applyFromDelta(viewModel, tracker, rg, json['FromState'], fromProperty, count);

    if (!ignored && toProperty && typeof tracker[toProperty] === 'function') {
        tracker[toProperty](tracker[toProperty]() + count);
    }
}

// terminalOnAll reports whether a to-state is a terminal state that must not be
// applied to the "+all+" live aggregate (which tracks only in-flight jobs).
function terminalOnAll(isAll, toState) {
    return isAll && (toState == 'complete' || toState == 'deleted');
}

// applyIgnoredToState consumes a pending "ignore" amount for this RepGroup's
// to-state (created when an earlier from-delta went negative) and returns true
// if the whole delta was absorbed by the ignore and should not increment the
// to-state.
function applyIgnoredToState(viewModel, rg, toState, count) {
    const rgIgnore = viewModel.ignore[rg];

    if (rgIgnore && rgIgnore.hasOwnProperty(toState) && rgIgnore[toState] >= count) {
        rgIgnore[toState] -= count;

        if (rgIgnore[toState] == 0) {
            delete rgIgnore[toState];

            if (Object.keys(rgIgnore).length == 0) {
                delete viewModel.ignore[rg];
            }
        }

        return true;
    }

    return false;
}

// applyFromDelta decrements a tracker's from-state count by the delta, clamping
// at zero and recording the shortfall as an amount to ignore from a future
// increment of that state (handles out-of-order deltas).
function applyFromDelta(viewModel, tracker, rg, fromState, fromProperty, count) {
    if (!fromProperty || typeof tracker[fromProperty] !== 'function') {
        return;
    }

    const newFrom = tracker[fromProperty]() - count;

    if (newFrom >= 0) {
        tracker[fromProperty](newFrom);

        return;
    }

    if (!viewModel.ignore[rg]) {
        viewModel.ignore[rg] = {};
    }

    viewModel.ignore[rg][fromState] = (viewModel.ignore[rg][fromState] || 0) + count;
    tracker[fromProperty](0);
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

function livePushValue(value, fallback) {
    if (value === null || value === undefined) {
        return fallback;
    }

    return value;
}

function mergeJobDetailsPushUpdate(existing, update) {
    const merged = Object.assign({}, existing, update);

    if (update.State === 'running' || update.State === 'reserved') {
        merged.Walltime = update.Walltime > 0 ? update.Walltime : existing.Walltime;
        merged.Started = livePushValue(update.Started, existing.Started);
        merged.Cmd = update.Cmd || existing.Cmd;
        merged.ExpectedRAM = update.ExpectedRAM > 0 ? update.ExpectedRAM : existing.ExpectedRAM;
        merged.ExpectedTime = update.ExpectedTime > 0 ? update.ExpectedTime : existing.ExpectedTime;
        merged.RequestedDisk = update.RequestedDisk > 0 ? update.RequestedDisk : existing.RequestedDisk;
        merged.Cores = update.Cores > 0 ? update.Cores : existing.Cores;
        merged.Attempts = update.Attempts > 0 ? update.Attempts : existing.Attempts;
    }

    return merged;
}

/**
 * Handles job details messages from the WebSocket
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data
 */
function handleJobDetailsMessage(viewModel, json) {
    normalizeJobStatusTimes(json);

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
                    const merged = mergeJobDetailsPushUpdate(job, json);

                    // Set up LiveWalltime for the job
                    setupLiveWalltime(merged, merged['Walltime'], viewModel);

                    // Simply replace the job at the same index
                    const index = jobs.indexOf(job);
                    viewModel.detailsOA.splice(index, 1, merged);
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
