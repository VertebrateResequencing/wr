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

        // Drop the occupancy-reconciliation model too: a reconnect re-seeds every
        // count from a fresh scan-on-connect, so any pending out-of-order exits or
        // occupancy left over from the previous connection must not carry across
        // and skew the new seed. handleStateChangeMessage lazily recreates it.
        delete tracker.__recon;
    }
}

function resetLiveCounts(viewModel) {
    // A reconnect re-seeds every count from a fresh scan-on-connect, so clear the
    // stale per-tracker reconciliation model (resetTrackerCounts does this via
    // delete tracker.__recon) as well as the count observables. The "+all+"
    // in-flight tracker and every non-search RepGroup tracker are reset.
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

    // On connect (and again on reconnect, since that is a fresh connection) we
    // send a "current" request. The server replies with the scan-on-connect
    // status-count seed (jstateCount deltas from the "new" state) and also
    // (re)broadcasts the recoverable bad-server and scheduler-issue sets. On a
    // reconnect the stale live counts (and the out-of-order reconciliation
    // model) are cleared first in onopen, so the fresh seed is applied cleanly.
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

// stateToProperty maps a wire JobState string to the tracker's display bucket
// (the count observable name). reserved merges into running. States that are
// only a creation source ("new") have no bucket and are absent, so a lookup
// yields undefined; the caller treats such a from-state as a job entering the
// to-state.
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

// ALL_BUCKETS is the full set of distinct display buckets, iterated when
// mirroring the internal occupancy onto a tracker's count observables.
const ALL_BUCKETS = ['delayed', 'dependent', 'suspended', 'ready', 'running', 'lost', 'buried', 'deleted', 'complete'];

// reconModel returns (lazily creating) the per-tracker occupancy-reconciliation
// state. occ[bucket] is how many jobs are currently in each bucket (always
// >= 0); pending[from][to] holds observed exits we cannot apply yet because we
// have not seen those jobs enter `from` (the out-of-order / pre-seed case). It
// is cleared on reconnect by resetTrackerCounts (delete tracker.__recon).
function reconModel(tracker) {
    if (!tracker.__recon) {
        tracker.__recon = { occ: Object.create(null), pending: Object.create(null) };
    }

    return tracker.__recon;
}

// isDisplayed reports whether a bucket has a bound count observable on this
// tracker. A RepGroup tracker has all buckets; the "+all+" in-flight tracker has
// only the live ones (no complete/deleted), so terminal buckets are tracked
// internally there but never shown.
function isDisplayed(tracker, bucket) {
    return typeof tracker[bucket] === 'function';
}

// isEmpty reports whether an object holds no positive counts, so a fully-drained
// pending entry can be dropped.
function isEmpty(obj) {
    for (const key in obj) {
        if (obj[key] > 0) {
            return false;
        }
    }

    return true;
}

// settle forwards a node's observed-but-unbacked exits (held in pending) into
// their destination buckets as far as the node's occupancy allows, cascading
// into each destination since a forwarded job may itself have a pending onward
// exit. It is iterative with an explicit work queue rather than recursive, so a
// transition cycle - e.g. a rerun's complete->ready->running->complete - cannot
// re-enter settle for a node whose occupancy is mid-update and corrupt it. Each
// move strictly reduces total pending, so it always terminates.
function settle(model, startNode) {
    const queue = [startNode];

    while (queue.length > 0) {
        const node = queue.shift();
        const pending = model.pending[node];
        if (!pending) {
            continue;
        }

        let occ = model.occ[node] || 0;
        if (occ <= 0) {
            continue;
        }

        for (const target in pending) {
            if (occ <= 0) {
                break;
            }

            const want = pending[target];
            if (!(want > 0)) {
                delete pending[target];
                continue;
            }

            const moved = Math.min(occ, want);
            occ -= moved;

            if (want - moved <= 0) {
                delete pending[target];
            } else {
                pending[target] = want - moved;
            }

            model.occ[target] = (model.occ[target] || 0) + moved;
            queue.push(target);
        }

        model.occ[node] = occ;
        if (isEmpty(pending)) {
            delete model.pending[node];
        }
    }
}

// syncDisplay mirrors the internal occupancy of the displayed buckets onto the
// tracker's count observables, writing only genuine changes so Knockout
// re-renders the affected bar segments smoothly rather than on every message.
function syncDisplay(tracker, model) {
    for (const bucket of ALL_BUCKETS) {
        if (!isDisplayed(tracker, bucket)) {
            continue;
        }

        const value = model.occ[bucket] || 0;
        if (tracker[bucket]() !== value) {
            tracker[bucket](value);
        }
    }
}

/**
 * Handles a from->to state-count delta message from the WebSocket by applying it
 * to an exact, order-independent occupancy model on the RepGroup tracker (and the
 * "+all+" in-flight tracker), then mirroring the displayed buckets onto the count
 * observables. The server emits deltas from concurrent goroutines, so they arrive
 * unordered and the scan-on-connect seed is unaligned with the live stream;
 * reconstructing occupancy (rather than blindly incrementing/decrementing the
 * observables) makes the counts independent of arrival order and free of both the
 * transient overcount/dip and the mid-burst permanent divergence of the old
 * ignore-map approach. "+all+" mirrors only the live buckets, so complete/deleted
 * are tracked internally there - a completing job leaves the live bar and a later
 * rerun re-adds it, reconciled exactly rather than double-counted.
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} json - The JSON message data ({ RepGroup, FromState, ToState, Count })
 */
function handleStateChangeMessage(viewModel, json) {
    const rg = json['RepGroup'];
    const isAll = rg == "+all+";
    const tracker = isAll ? viewModel.inflight : getOrCreateRepGroupTracker(viewModel, rg);
    const count = json['Count'];
    if (!(count > 0)) {
        return;
    }

    const model = reconModel(tracker);
    const fromBucket = stateToProperty[json['FromState']]; // undefined for "new"
    const toBucket = stateToProperty[json['ToState']] || json['ToState'];

    if (fromBucket === undefined) {
        // Creation or re-entry from an untracked origin ("new"): the jobs simply
        // enter `to`, then settle drains any exits already waiting on that bucket.
        model.occ[toBucket] = (model.occ[toBucket] || 0) + count;
        settle(model, toBucket);
    } else if (fromBucket !== toBucket) {
        // A normal exit: record it, then forward it as occupancy allows. If we
        // have not yet seen these jobs enter `from` (an out-of-order or pre-seed
        // delta) the exit waits in pending, so `to` is never credited a job whose
        // presence in `from` has not been observed (which is what caused the old
        // transient overcount).
        if (!model.pending[fromBucket]) {
            model.pending[fromBucket] = Object.create(null);
        }

        model.pending[fromBucket][toBucket] = (model.pending[fromBucket][toBucket] || 0) + count;
        settle(model, fromBucket);
    }
    // fromBucket === toBucket (e.g. reserved<->running, which share the running
    // bucket) is a no-op.

    syncDisplay(tracker, model);
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
