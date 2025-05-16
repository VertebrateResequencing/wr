/* Status View Model
 * Main view model for the WR status page.
 */

// viewmodel for displaying status
function StatusViewModel() {
    var self = this;

    //-------------------------------------------------------------------------
    // PROPERTIES AND BASIC OBSERVABLES
    //-------------------------------------------------------------------------
    self.token = getParameterByName("token");
    self.aquiringstatus = ko.observableArray();
    self.statuserror = ko.observableArray();
    self.badservers = ko.observableArray();
    self.messages = ko.observableArray();
    self.repGroup = ko.observable();
    self.detailsRepgroup = '';
    self.detailsState = '';
    self.detailsOA;
    self.wallTimeUpdater;
    self.wallTimeUpdaters = new Array();
    self.rateLimit = 350;
    self.currentLimit = 1;
    self.repGroups = [];
    self.repGroupLookup = {};
    self.sortableRepGroups = ko.observableArray();
    self.ignore = {};

    //-------------------------------------------------------------------------
    // UTILITY FUNCTIONS
    //-------------------------------------------------------------------------
    self.removeBadServer = function (id) {
        self.badservers.remove(function (server) {
            return server.ID == id;
        });
    }

    self.removeMessage = function (msg) {
        self.messages.remove(function (schedIssue) {
            return schedIssue.Msg == msg;
        });
    }

    //-------------------------------------------------------------------------
    // IN-FLIGHT JOB TRACKING
    //-------------------------------------------------------------------------
    self.inflight = {
        'delayed': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'dependent': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'ready': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'running': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'lost': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'buried': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'delayPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'dependentPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'readyPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'runPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'lostPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'buryPct': ko.observable(0).extend({ rateLimit: self.rateLimit }),
        'old_total': 0,
        'delay_compute': 0
    };
    self.inflight['total'] = ko.computed(function () {
        if (self.inflight['delay_compute']) {
            return self.inflight['old_total'];
        }

        var total = self.inflight['delayed']() + self.inflight['dependent']() + self.inflight['ready']() + self.inflight['running']() + self.inflight['lost']() + self.inflight['buried']();
        if (total > 0) {
            var multiplier = 100 / total;
            // we scale to 98 to avoid a bug in bootstrap progress
            // bars which will result in the right-most bar
            // flickering out of existence, even though we never
            // total over 100
            var scaled = percentScaler([(multiplier * self.inflight['delayed']()), (multiplier * self.inflight['dependent']()), (multiplier * self.inflight['ready']()), (multiplier * self.inflight['running']()), (multiplier * self.inflight['lost']()), (multiplier * self.inflight['buried']())], 98);
            var rounded = percentRounder(scaled, 2);
            self.inflight['delayPct'](rounded[0]);
            self.inflight['dependentPct'](rounded[1]);
            self.inflight['readyPct'](rounded[2]);
            self.inflight['runPct'](rounded[3]);
            self.inflight['lostPct'](rounded[4]);
            self.inflight['buryPct'](rounded[5]);
        }

        self.inflight['old_total'] = total;
        return total;
    }).extend({ rateLimit: self.rateLimit });

    //-------------------------------------------------------------------------
    // WEBSOCKET SETUP AND MESSAGE HANDLING
    //-------------------------------------------------------------------------
    // set up the websocket
    if (window.WebSocket === undefined) {
        self.statuserror.push("Your browser does not support WebSockets");
    } else {
        self.ws = new WebSocket("wss://" + location.hostname + ":" + location.port + "/status_ws?token=" + self.token);
        self.ws.onopen = function () {
            self.ws.send(JSON.stringify({ Request: "current" }));
        };
        self.ws.onclose = function () {
            self.statuserror.push("Connection to the manager has been lost!");
            //*** we could poll and try to re-establish the connection...
        }
        self.ws.onmessage = function (e) {
            json = JSON.parse(e.data)
            if (json.hasOwnProperty('FromState')) {
                // state numbers have changed
                rg = json['RepGroup']
                var repgroup
                if (rg == "+all+") {
                    repgroup = self.inflight;
                } else if (self.repGroupLookup.hasOwnProperty(rg)) {
                    repgroup = self.repGroups[self.repGroupLookup[rg]];
                } else {
                    repgroup = {
                        'id': rg,
                        'delayed': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'dependent': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'ready': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'running': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'lost': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'buried': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'deleted': ko.observable(0).extend({ rateLimit: self.rateLimit }),
                        'complete': ko.observable(0).extend({ rateLimit: self.rateLimit }),
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

                        var total = repgroup['delayed']() + repgroup['dependent']() + repgroup['ready']() + repgroup['running']() + repgroup['lost']() + repgroup['buried']() + repgroup['deleted']() + repgroup['complete']();
                        if (total > 0) {
                            var multiplier = 100 / total;
                            // we scale to 98 to avoid a bug in
                            // bootstrap progress bars which will
                            // result in the right-most bar
                            // flickering out of existence, even
                            // though we never total over 100
                            var scaled = percentScaler([(multiplier * repgroup['delayed']()), (multiplier * repgroup['dependent']()), (multiplier * repgroup['ready']()), (multiplier * repgroup['running']()), (multiplier * repgroup['lost']()), (multiplier * repgroup['buried']()), (multiplier * repgroup['deleted']()), (multiplier * repgroup['complete']())], 98);
                            var rounded = percentRounder(scaled, 2);

                            // to avoid the percentage bars
                            // totalling over 100 at any point in
                            // time, do all decrementing updates
                            // first; not sure if this really helps
                            // avoid some instances of flickering,
                            // but it might...
                            var keys = ['delayPct', 'dependentPct', 'readyPct', 'runPct', 'lostPct', 'buryPct', 'deletePct', 'completePct'];
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
                    }).extend({ rateLimit: self.rateLimit });

                    self.repGroups.push(repgroup);
                    self.repGroupLookup[rg] = self.repGroups.length - 1;

                    // because of our lookup, repGroup sort order
                    // must not change, but we want them displayed
                    // sorted, so we also push to an independent
                    // observableArray
                    self.sortableRepGroups.push(repgroup);
                }

                var from, to
                switch (json['FromState']) {
                    case 'delayed':
                        from = repgroup['delayed'];
                        break;
                    case 'dependent':
                        from = repgroup['dependent'];
                        break;
                    case 'ready':
                        from = repgroup['ready'];
                        break;
                    case 'running':
                        from = repgroup['running'];
                        break;
                    case 'lost':
                        from = repgroup['lost'];
                        break;
                    case 'buried':
                        from = repgroup['buried'];
                        break;
                }

                if (self.ignore.hasOwnProperty(json['RepGroup']) && self.ignore[json['RepGroup']].hasOwnProperty(json['ToState']) && self.ignore[json['RepGroup']][json['ToState']] >= json['Count']) {
                    // ignore this 'to' transition, because things
                    // are out of order and we already accounted for
                    // it
                    self.ignore[json['RepGroup']][json['ToState']] -= json['Count'];
                    if (self.ignore[json['RepGroup']][json['ToState']] == 0) {
                        delete self.ignore[json['RepGroup']][json['ToState']];
                        if (Object.keys(self.ignore[json['RepGroup']]).length == 0) {
                            delete self.ignore[json['RepGroup']];
                        }
                    }
                } else {
                    switch (json['ToState']) {
                        case 'delayed':
                            to = repgroup['delayed'];
                            break;
                        case 'dependent':
                            to = repgroup['dependent'];
                            break;
                        case 'ready':
                            to = repgroup['ready'];
                            break;
                        case 'running':
                            to = repgroup['running'];
                            break;
                        case 'lost':
                            to = repgroup['lost'];
                            break;
                        case 'buried':
                            to = repgroup['buried'];
                            break;
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

                repgroup['delay_compute'] = to ? 1 : 0;
                if (from) {
                    var newfrom = from() - json['Count'];
                    if (newfrom >= 0) {
                        from(newfrom);
                    } else {
                        // sometimes transitions can arrive out of
                        // order; we'll let this one go through, and
                        // mark to ignore the previous transition
                        // when it comes in later
                        if (!self.ignore.hasOwnProperty(json['RepGroup'])) {
                            self.ignore[json['RepGroup']] = {};
                        }
                        if (self.ignore[json['RepGroup']].hasOwnProperty(json['FromState'])) {
                            self.ignore[json['RepGroup']][json['FromState']] += json['Count'];
                        } else {
                            self.ignore[json['RepGroup']][json['FromState']] = json['Count'];
                        }

                        from(0);
                    }
                }
                repgroup['delay_compute'] = 0;
                if (to) {
                    to(to() + json['Count']);
                }
            } else if (json.hasOwnProperty('State')) {
                rg = json['RepGroup']
                if (self.detailsOA && rg == self.detailsRepgroup) {
                    // the user has clicked on a progress bar for
                    // a particular repgroup; add to its details
                    var walltime = json['Walltime'];
                    if (json['State'] == "running") {
                        // have Walltime on running jobs auto-
                        // increment
                        var began = new Date();
                        var now = ko.observable(new Date());
                        json['LiveWalltime'] = ko.computed(function () {
                            return walltime + ((now() - began) / 1000);
                        });
                        self.wallTimeUpdaters.push(now)
                        if (!self.wallTimeUpdater) {
                            self.wallTimeUpdater = window.setInterval(function () {
                                var arrayLength = self.wallTimeUpdaters.length;
                                for (var i = 0; i < arrayLength; i++) {
                                    self.wallTimeUpdaters[i](new Date());
                                };
                            }, 1000);
                        }
                    } else {
                        json['LiveWalltime'] = ko.computed(function () {
                            return walltime;
                        });
                    }
                    self.detailsOA.push(json);
                }
            } else if (json.hasOwnProperty('IP')) {
                // it's either a new bad server, or an existing
                // bad server that is now fine
                if (json['IsBad']) {
                    self.badservers.push(json);
                } else {
                    self.removeBadServer(json['ID'])
                }
            } else if (json.hasOwnProperty('Msg')) {
                // it's either a new scheduler message, or we want
                // to update one we're already displaying
                var updated = false
                var messages = self.messages();
                for (var i = 0; i < messages.length; ++i) {
                    var si = messages[i];
                    if (si.Msg == json['Msg']) {
                        si.LastDate(json['LastDate'])
                        si.Count(json['Count'])
                        updated = true
                        break
                    }
                }

                if (!updated) {
                    var schedIssue = {
                        'Msg': json['Msg'],
                        'FirstDate': json['FirstDate'],
                        'LastDate': ko.observable(json['LastDate']),
                        'Count': ko.observable(json['Count']),
                    }
                    self.messages.push(schedIssue);
                }
            }
        }
    }

    //-------------------------------------------------------------------------
    // REPGROUP HANDLING
    //-------------------------------------------------------------------------
    // act if the user requests a repGroup
    self.requestRepGroup = function (formElement) {
        console.log("requesting rep group " + self.repGroup())
        self.ws.send(JSON.stringify({ Request: 'search', RepGroup: self.repGroup() }));
        // *** not yet implemented in the manager, does nothing
    };

    // act if the user clicks on the different types of progress
    // bar for a repGroup
    self.showRepgroupDelayed = function (repGroup) {
        self.showGroupState(repGroup, 'delayed');
    };
    self.showRepgroupDependent = function (repGroup) {
        self.showGroupState(repGroup, 'dependent');
    };
    self.showRepgroupReady = function (repGroup) {
        self.showGroupState(repGroup, 'ready');
    };
    self.showRepgroupRunning = function (repGroup) {
        self.showGroupState(repGroup, 'reserved'); // which includes 'running'
    };
    self.showRepgroupLost = function (repGroup) {
        self.showGroupState(repGroup, 'lost');
    };
    self.showRepgroupBuried = function (repGroup) {
        self.showGroupState(repGroup, 'buried');
    };
    self.showRepgroupComplete = function (repGroup) {
        self.showGroupState(repGroup, 'complete');
    };
    self.showGroupState = function (repGroup, state) {
        if (self.detailsOA) {
            if (self.wallTimeUpdater) {
                self.wallTimeUpdaters = new Array();
                window.clearInterval(self.wallTimeUpdater);
                self.wallTimeUpdater = '';
            }
            self.detailsOA([]);
        }

        if (repGroup.id == self.detailsRepgroup && state == self.detailsState) {
            // stop showing
            repGroup.details([]);
            self.detailsRepgroup = '';
            self.detailsState = '';
            self.detailsOA = '';
            self.currentLimit = 1; // Reset limit when closing details
            return;
        }

        self.detailsRepgroup = repGroup.id;
        self.detailsState = state;
        self.detailsOA = repGroup.details;
        self.currentLimit = 1; // Start with a limit of 1
        self.ws.send(JSON.stringify({ Request: 'details', RepGroup: repGroup.id, State: state, Limit: self.currentLimit }));
    }

    //-------------------------------------------------------------------------
    // MODAL DISPLAY HANDLERS
    //-------------------------------------------------------------------------
    // act if the user clicks to view LimitGroups
    self.lgModalVisible = ko.observable(false);
    self.lgVars = ko.observableArray();
    self.showLimitGroups = function (job) {
        self.lgVars(job.LimitGroups);
        self.lgModalVisible(true);
    }

    // act if the user clicks to view DepGroups
    self.dgModalVisible = ko.observable(false);
    self.dgVars = ko.observableArray();
    self.showDepGroups = function (job) {
        self.dgVars(job.DepGroups);
        self.dgModalVisible(true);
    }

    // act if the user clicks to view Dependencies
    self.depModalVisible = ko.observable(false);
    self.depVars = ko.observableArray();
    self.showDependencies = function (job) {
        self.depVars(job.Dependencies);
        self.depModalVisible(true);
    }

    // act if the user clicks to view Behaviours
    self.behModalVisible = ko.observable(false);
    self.behVars = ko.observableArray();
    self.showBehaviours = function (job) {
        self.behVars([job.Behaviours]);
        self.behModalVisible(true);
    }

    // act if the user clicks to view Other
    self.otherModalVisible = ko.observable(false);
    self.otherVars = ko.observableArray();
    self.showOther = function (job) {
        self.otherVars(job.OtherRequests);
        self.otherModalVisible(true);
    }

    // act if the user clicks to view InternalID
    self.internalModalVisible = ko.observable(false);
    self.internalVars = ko.observableArray();
    self.showInternalID = function (job) {
        self.internalVars([job.Key]);
        self.internalModalVisible(true);
    }

    // act if the user clicks to view env
    self.envModalVisible = ko.observable(false);
    self.envVars = ko.observableArray();
    self.showEnv = function (job) {
        self.envVars(job.Env);
        self.envModalVisible(true);
    }

    //-------------------------------------------------------------------------
    // ACTION HANDLING
    //-------------------------------------------------------------------------
    // act if the user clicks one of the action buttons in the
    // details of a progress bar
    self.actionModalVisible = ko.observable(false);
    self.actionModalHeader = ko.observable();
    self.actionDetails = {
        action: ko.observable(),
        button: ko.observable(),
        key: ko.observable(),
        repGroup: ko.observable(),
        state: ko.observable(),
        exited: ko.observable(),
        exitCode: ko.observable(),
        failReason: ko.observable(),
        count: ko.observable()
    };
    self.jobToActionDetails = function (job, action, button) {
        self.actionDetails.action(action);
        self.actionDetails.button(button);
        self.actionDetails.key(job.Key);
        self.actionDetails.repGroup(job.RepGroup);
        self.actionDetails.state(job.State);
        self.actionDetails.exitCode(job.Exited);
        self.actionDetails.exitCode(job.Exitcode);
        self.actionDetails.failReason(job.FailReason);
        self.actionDetails.count(job.Similar + 1);
    };
    self.commitAction = function (all) {
        // request the action
        if (all) {
            self.ws.send(JSON.stringify({
                Request: self.actionDetails.action(),
                RepGroup: self.actionDetails.repGroup(),
                State: self.actionDetails.state(),
                Exitcode: self.actionDetails.exitCode(),
                FailReason: self.actionDetails.failReason(),
            }));
        } else {
            self.ws.send(JSON.stringify({
                Request: self.actionDetails.action(),
                Key: self.actionDetails.key(),
            }));
        }

        // reset the ui
        if (self.detailsOA) {
            self.detailsOA([]);
        }
        self.detailsRepgroup = '';
        self.detailsState = '';
        self.detailsOA = '';
        self.actionModalVisible(false);
    };

    //-------------------------------------------------------------------------
    // ACTION CONFIRMATION HANDLERS
    //-------------------------------------------------------------------------
    self.confirmRetry = function (job) {
        self.jobToActionDetails(job, 'retry', 'retry');
        self.actionModalHeader('Retry Buried Commands');
        self.actionModalVisible(true);
    };
    self.confirmRemoveFail = function (job) {
        self.jobToActionDetails(job, 'remove', 'remove');
        self.actionModalHeader('Remove Failed Commands');
        self.actionModalVisible(true);
    };
    self.confirmRemoveDep = function (job) {
        self.jobToActionDetails(job, 'remove', 'remove');
        self.actionModalHeader('Remove Dependent Commands');
        self.actionModalVisible(true);
    };
    self.confirmRemovePend = function (job) {
        self.jobToActionDetails(job, 'remove', 'remove');
        self.actionModalHeader('Remove Pending Commands');
        self.actionModalVisible(true);
    };
    self.confirmRemoveDelay = function (job) {
        self.jobToActionDetails(job, 'remove', 'remove');
        self.actionModalHeader('Remove Delayed Commands');
        self.actionModalVisible(true);
    };
    self.confirmKill = function (job) {
        self.jobToActionDetails(job, 'kill', 'kill');
        self.actionModalHeader('Kill Running Commands');
        self.actionModalVisible(true);
    };
    self.confirmDead = function (job) {
        self.jobToActionDetails(job, 'kill', 'confirm');
        self.actionModalHeader('Confirm Commands are Dead');
        self.actionModalVisible(true);
    };

    //-------------------------------------------------------------------------
    // SERVER MANAGEMENT
    //-------------------------------------------------------------------------
    // act if the user confirms that a server is dead
    self.confirmDeadServer = function (server) {
        self.ws.send(JSON.stringify({ Request: 'confirmBadServer', ServerID: server.ID }));
        self.removeBadServer(server.ID)
    };

    //-------------------------------------------------------------------------
    // MESSAGE HANDLING
    //-------------------------------------------------------------------------
    // act if the user dismisses a message
    self.dismissMessage = function (si) {
        self.ws.send(JSON.stringify({ Request: 'dismissMsg', Msg: si.Msg }));
        self.removeMessage(si.Msg)
    };
    self.dismissMessages = function (si) {
        self.ws.send(JSON.stringify({ Request: 'dismissMsgs' }));
        self.messages([])
    };

    //-------------------------------------------------------------------------
    // JOB LOADING
    //-------------------------------------------------------------------------
    self.loadMoreJobs = function () {
        // Increase the limit by a reasonable number (e.g., 5 more at a time)
        self.currentLimit += 5;

        // Clear the current details to avoid duplicates
        if (self.detailsOA) {
            self.detailsOA([]);
        }

        // Request more jobs with the increased limit
        self.ws.send(JSON.stringify({
            Request: 'details',
            RepGroup: self.detailsRepgroup,
            State: self.detailsState,
            Limit: self.currentLimit
        }));
    };
}