/* In-Flight Job Tracking
 * Handles tracking of in-flight jobs in the WR status page.
 */
import { percentRounder, percentScaler } from '/js/utility.js';

// Define the standard property sets first
const STANDARD_COUNT_PROPS = ['delayed', 'dependent', 'ready', 'running', 'lost', 'buried'];

// Define RepGroup properties by extending the standard ones
const REPGROUP_COUNT_PROPS = [...STANDARD_COUNT_PROPS, 'deleted', 'complete'];

// Define corresponding percentage property names
const STANDARD_PCT_PROPS = ['delayPct', 'dependentPct', 'readyPct', 'runPct', 'lostPct', 'buryPct'];
const REPGROUP_PCT_PROPS = [...STANDARD_PCT_PROPS, 'deletePct', 'completePct'];

/**
 * Updates progress bar percentages based on counts
 * @param {Object} tracker - The tracker object with observables
 * @param {Array<string>} countProps - Array of property names for count observables
 * @param {Array<string>} pctProps - Array of property names for percentage observables
 * @returns {boolean} True if values were updated
 */
function updateProgressBars(tracker, countProps, pctProps) {
    // Calculate total from all count properties
    const counts = countProps.map(prop => tracker[prop]());
    const total = counts.reduce((sum, count) => sum + count, 0);

    if (total <= 0) return false;

    // Calculate percentages
    const multiplier = 100 / total;
    const values = counts.map(count => multiplier * count);

    // Hardcoded scale to 99.9% to avoid bootstrap's progress bar flickering
    const scaled = percentScaler(values, 99.9);

    // Ensure percentages sum correctly
    const rounded = percentRounder(scaled, 2);

    // Get current percentage values for comparison
    const currentValues = pctProps.map(prop => tracker[prop]());

    // Only update if values have changed significantly
    const hasChanged = rounded.some((val, i) => Math.abs(val - currentValues[i]) > 0.01);

    if (hasChanged) {
        // Create batch update function
        const batchUpdate = () => {
            // Update all percentage properties at once
            pctProps.forEach((prop, i) => {
                tracker[prop](rounded[i]);
            });
        };

        // Use requestAnimationFrame for smoother updates
        if (window.requestAnimationFrame) {
            window.requestAnimationFrame(batchUpdate);
        } else {
            batchUpdate();
        }

        return true;
    }

    return false;
}

/**
 * Creates and configures in-flight job tracking observables
 * @param {number} rateLimit - The rate limit for updates
 * @returns {object} The configured in-flight tracking object
 */
export function setupInflightTracking(rateLimit) {
    const inflight = {
        delayed: ko.observable(0).extend({ rateLimit }),
        dependent: ko.observable(0).extend({ rateLimit }),
        ready: ko.observable(0).extend({ rateLimit }),
        running: ko.observable(0).extend({ rateLimit }),
        lost: ko.observable(0).extend({ rateLimit }),
        buried: ko.observable(0).extend({ rateLimit }),
        delayPct: ko.observable(0).extend({ rateLimit }),
        dependentPct: ko.observable(0).extend({ rateLimit }),
        readyPct: ko.observable(0).extend({ rateLimit }),
        runPct: ko.observable(0).extend({ rateLimit }),
        lostPct: ko.observable(0).extend({ rateLimit }),
        buryPct: ko.observable(0).extend({ rateLimit }),
        old_total: 0
    };

    // Create a computed for the total that updates the percentage values
    inflight.total = ko.computed(() => {
        const total = inflight.delayed() + inflight.dependent() +
            inflight.ready() + inflight.running() +
            inflight.lost() + inflight.buried();

        if (total > 0) {
            updateProgressBars(inflight, STANDARD_COUNT_PROPS, STANDARD_PCT_PROPS);
        }

        inflight.old_total = total;
        return total;
    }).extend({ rateLimit });

    return inflight;
}

/**
 * Creates a new rep group tracking object
 * @param {string} rg - The rep group ID
 * @param {number} rateLimit - The rate limit for updates
 * @returns {object} A configured rep group tracking object
 */
export function createRepGroupTracker(rg, rateLimit) {
    const repgroup = {
        id: rg,
        delayed: ko.observable(0).extend({ rateLimit }),
        dependent: ko.observable(0).extend({ rateLimit }),
        ready: ko.observable(0).extend({ rateLimit }),
        running: ko.observable(0).extend({ rateLimit }),
        lost: ko.observable(0).extend({ rateLimit }),
        buried: ko.observable(0).extend({ rateLimit }),
        deleted: ko.observable(0).extend({ rateLimit }),
        complete: ko.observable(0).extend({ rateLimit }),
        delayPct: ko.observable(0),
        dependentPct: ko.observable(0),
        readyPct: ko.observable(0),
        runPct: ko.observable(0),
        lostPct: ko.observable(0),
        buryPct: ko.observable(0),
        deletePct: ko.observable(0),
        completePct: ko.observable(0),
        details: ko.observableArray(),
        old_total: 0
    };

    repgroup.total = ko.computed(() => {
        const total = repgroup.delayed() + repgroup.dependent() +
            repgroup.ready() + repgroup.running() +
            repgroup.lost() + repgroup.buried() +
            repgroup.deleted() + repgroup.complete();

        if (total > 0) {
            updateProgressBars(repgroup, REPGROUP_COUNT_PROPS, REPGROUP_PCT_PROPS);
        }

        repgroup.old_total = total;
        return total;
    }).extend({ rateLimit });

    return repgroup;
}
