/* In-Flight Job Tracking
 * Handles tracking of in-flight jobs in the WR status page.
 */
import { percentRounder, percentScaler } from '/js/utility.js';

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
        old_total: 0,
        delay_compute: 0
    };

    // Create a computed for the total that updates the percentage values
    inflight.total = ko.computed(() => {
        if (inflight.delay_compute) {
            return inflight.old_total;
        }

        const total = inflight.delayed() + inflight.dependent() +
            inflight.ready() + inflight.running() +
            inflight.lost() + inflight.buried();

        if (total > 0) {
            const multiplier = 100 / total;

            const values = [
                (multiplier * inflight.delayed()),
                (multiplier * inflight.dependent()),
                (multiplier * inflight.ready()),
                (multiplier * inflight.running()),
                (multiplier * inflight.lost()),
                (multiplier * inflight.buried())
            ];

            // Make sure values sum to exactly 99.99% (not 100%) to avoid the flickering issue
            // while still visually filling the entire bar
            const rounded = percentRounder(values, 2);

            // Set values with slight delay between them to avoid rendering conflicts
            setTimeout(() => inflight.delayPct(rounded[0]), 0);
            setTimeout(() => inflight.dependentPct(rounded[1]), 10);
            setTimeout(() => inflight.readyPct(rounded[2]), 20);
            setTimeout(() => inflight.runPct(rounded[3]), 30);
            setTimeout(() => inflight.lostPct(rounded[4]), 40);
            setTimeout(() => inflight.buryPct(rounded[5]), 50);
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
        old_total: 0,
        delay_compute: 0
    };

    repgroup.total = ko.computed(() => {
        if (repgroup.delay_compute) {
            return repgroup.old_total;
        }

        const total = repgroup.delayed() + repgroup.dependent() +
            repgroup.ready() + repgroup.running() +
            repgroup.lost() + repgroup.buried() +
            repgroup.deleted() + repgroup.complete();

        if (total > 0) {
            const multiplier = 100 / total;

            // Calculate percentages at full 100% scale
            const values = [
                (multiplier * repgroup.delayed()),
                (multiplier * repgroup.dependent()),
                (multiplier * repgroup.ready()),
                (multiplier * repgroup.running()),
                (multiplier * repgroup.lost()),
                (multiplier * repgroup.buried()),
                (multiplier * repgroup.deleted()),
                (multiplier * repgroup.complete())
            ];

            // Make sure values sum to exactly 99.99% (not 100%) to avoid the flickering issue
            const rounded = percentRounder(values, 2);

            // Define the keys that will be updated
            const keys = ['delayPct', 'dependentPct', 'readyPct', 'runPct',
                'lostPct', 'buryPct', 'deletePct', 'completePct'];

            // Update percentage values with slight delays to avoid rendering conflicts
            for (let i = 0; i < keys.length; i++) {
                // Stagger updates with timeouts to prevent flickering
                ((index) => {
                    setTimeout(() => repgroup[keys[index]](rounded[index]), index * 10);
                })(i);
            }
        }

        repgroup.old_total = total;
        return total;
    }).extend({ rateLimit });

    return repgroup;
}
