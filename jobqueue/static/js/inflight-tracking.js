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
    var inflight = {
        'delayed': ko.observable(0).extend({ rateLimit: rateLimit }),
        'dependent': ko.observable(0).extend({ rateLimit: rateLimit }),
        'ready': ko.observable(0).extend({ rateLimit: rateLimit }),
        'running': ko.observable(0).extend({ rateLimit: rateLimit }),
        'lost': ko.observable(0).extend({ rateLimit: rateLimit }),
        'buried': ko.observable(0).extend({ rateLimit: rateLimit }),
        'delayPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'dependentPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'readyPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'runPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'lostPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'buryPct': ko.observable(0).extend({ rateLimit: rateLimit }),
        'old_total': 0,
        'delay_compute': 0
    };

    // Create a computed for the total that updates the percentage values
    inflight['total'] = ko.computed(function () {
        if (inflight['delay_compute']) {
            return inflight['old_total'];
        }

        var total = inflight['delayed']() + inflight['dependent']() +
            inflight['ready']() + inflight['running']() +
            inflight['lost']() + inflight['buried']();

        if (total > 0) {
            var multiplier = 100 / total;
            // We scale to 98 to avoid a bug in bootstrap progress
            // bars which will result in the right-most bar
            // flickering out of existence, even though we never
            // total over 100
            var scaled = percentScaler([
                (multiplier * inflight['delayed']()),
                (multiplier * inflight['dependent']()),
                (multiplier * inflight['ready']()),
                (multiplier * inflight['running']()),
                (multiplier * inflight['lost']()),
                (multiplier * inflight['buried']())
            ], 98);

            var rounded = percentRounder(scaled, 2);
            inflight['delayPct'](rounded[0]);
            inflight['dependentPct'](rounded[1]);
            inflight['readyPct'](rounded[2]);
            inflight['runPct'](rounded[3]);
            inflight['lostPct'](rounded[4]);
            inflight['buryPct'](rounded[5]);
        }

        inflight['old_total'] = total;
        return total;
    }).extend({ rateLimit: rateLimit });

    return inflight;
}

/**
 * Creates a new rep group tracking object
 * @param {string} rg - The rep group ID
 * @param {number} rateLimit - The rate limit for updates
 * @returns {object} A configured rep group tracking object
 */
export function createRepGroupTracker(rg, rateLimit) {
    var repgroup = {
        'id': rg,
        'delayed': ko.observable(0).extend({ rateLimit: rateLimit }),
        'dependent': ko.observable(0).extend({ rateLimit: rateLimit }),
        'ready': ko.observable(0).extend({ rateLimit: rateLimit }),
        'running': ko.observable(0).extend({ rateLimit: rateLimit }),
        'lost': ko.observable(0).extend({ rateLimit: rateLimit }),
        'buried': ko.observable(0).extend({ rateLimit: rateLimit }),
        'deleted': ko.observable(0).extend({ rateLimit: rateLimit }),
        'complete': ko.observable(0).extend({ rateLimit: rateLimit }),
        'delayPct': ko.observable(0),
        'dependentPct': ko.observable(0),
        'readyPct': ko.observable(0),
        'runPct': ko.observable(0),
        'lostPct': ko.observable(0),
        'buryPct': ko.observable(0),
        'deletePct': ko.observable(0),
        'completePct': ko.observable(0),
        'details': ko.observableArray(),
        'old_total': 0,
        'delay_compute': 0
    };

    repgroup['total'] = ko.computed(function () {
        if (repgroup['delay_compute']) {
            return repgroup['old_total'];
        }

        var total = repgroup['delayed']() + repgroup['dependent']() +
            repgroup['ready']() + repgroup['running']() +
            repgroup['lost']() + repgroup['buried']() +
            repgroup['deleted']() + repgroup['complete']();

        if (total > 0) {
            var multiplier = 100 / total;
            var scaled = percentScaler([
                (multiplier * repgroup['delayed']()),
                (multiplier * repgroup['dependent']()),
                (multiplier * repgroup['ready']()),
                (multiplier * repgroup['running']()),
                (multiplier * repgroup['lost']()),
                (multiplier * repgroup['buried']()),
                (multiplier * repgroup['deleted']()),
                (multiplier * repgroup['complete']())
            ], 98);

            var rounded = percentRounder(scaled, 2);

            // To avoid the percentage bars totalling over 100 at any point in
            // time, do all decrementing updates first
            var keys = ['delayPct', 'dependentPct', 'readyPct', 'runPct',
                'lostPct', 'buryPct', 'deletePct', 'completePct'];

            for (var i = 0; i < 8; i++) {
                if (repgroup[keys[i]]() > rounded[i]) {
                    repgroup[keys[i]](rounded[i]);
                }
            }
            for (var i = 0; i < 8; i++) {
                if (repgroup[keys[i]]() < rounded[i]) {
                    repgroup[keys[i]](rounded[i]);
                }
            }
        }

        repgroup['old_total'] = total;
        return total;
    }).extend({ rateLimit: rateLimit });

    return repgroup;
}
