/* Status View Model
 * Main view model for the WR status page.
 */
import { getParameterByName } from '/js/wr/utility.js';
import { setupInflightTracking, createRepGroupTracker } from '/js/wr/inflight-tracking.js';
import { setupWebSocket } from '/js/wr/websocket-handler.js';
import { requestRepGroup, showGroupState, loadMoreJobs } from '/js/wr/repgroup-handler.js';
import { modalHandlers, jobToActionDetails, commitAction } from '/js/wr/modal-handlers.js';
import { actionHandlers } from '/js/wr/action-handlers.js';

// viewmodel for displaying status
export function StatusViewModel() {
    var self = this;

    //-------------------------------------------------------------------------
    // PROPERTIES AND BASIC OBSERVABLES
    //-------------------------------------------------------------------------
    self.token = getParameterByName("token");
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
    self.currentOffset = 0; // Only used for initial loading
    self.offsetMap = {}; // Track offsets per exitCode+reason
    self.newJobsInfo = {}; // Map to track batches of new jobs by exitCode+reason
    self.repGroups = [];
    self.repGroupLookup = {};
    self.sortableRepGroups = ko.observableArray();
    self.ignore = {};

    //-------------------------------------------------------------------------
    // SEARCH FUNCTIONALITY
    //-------------------------------------------------------------------------
    self.searchRepGroup = ko.observable('');
    self.searchIsSubstring = ko.observable(false);
    self.searchResults = ko.observableArray([]);
    self.searchSummaries = ko.observableArray([]);
    self.hasSearched = ko.observable(false);
    self.isSearchMode = ko.observable(false);

    // Show jobs from a search result as a RepGroup
    self.showGroupJobs = function (summary) {
        // First check if a repgroup already exists with this name
        let existingGroupIndex = -1;
        for (let i = 0; i < self.repGroups.length; i++) {
            if (self.repGroups[i].id === `search:${summary.name}`) {
                existingGroupIndex = i;
                break;
            }
        }

        // If found, remove it (we'll recreate it with updated data)
        if (existingGroupIndex >= 0) {
            const repgroupIndex = existingGroupIndex;
            self.sortableRepGroups.remove(self.repGroups[repgroupIndex]);
            self.repGroups.splice(repgroupIndex, 1);
            delete self.repGroupLookup[`search:${summary.name}`];

            // Adjust the lookup indices for repgroups after the removed one
            for (const rgId in self.repGroupLookup) {
                if (self.repGroupLookup[rgId] > repgroupIndex) {
                    self.repGroupLookup[rgId]--;
                }
            }
        }

        // Create a new repgroup using the summary information
        const repgroup = createRepGroupTracker(`search:${summary.name}`, self.rateLimit);

        // Update the counts to match our summary
        repgroup.complete(summary.counts.complete || 0);
        repgroup.buried(summary.counts.buried || 0);
        repgroup.delayed(summary.counts.delayed || 0);
        repgroup.dependent(summary.counts.dependent || 0);
        repgroup.ready(summary.counts.ready || 0);
        repgroup.running(summary.counts.running || 0);
        repgroup.lost(summary.counts.lost || 0);
        repgroup.deleted(0); // Not tracked in summary

        // Add the jobs to the details
        const jobsToAdd = [];

        // Filter jobs based on the summary name
        self.searchResults().forEach(job => {
            if ((summary.name === job.RepGroup) || (summary.name === "all above")) {
                // Clone the job to avoid reference issues
                const clonedJob = JSON.parse(JSON.stringify(job));

                // Add LiveWalltime for running jobs
                if (clonedJob.State === "running") {
                    const walltime = clonedJob.Walltime;
                    const began = new Date();
                    const now = ko.observable(new Date());

                    clonedJob.LiveWalltime = ko.computed(function () {
                        return walltime + ((now() - began) / 1000);
                    });

                    self.wallTimeUpdaters.push(now);

                    if (!self.wallTimeUpdater) {
                        self.wallTimeUpdater = window.setInterval(function () {
                            var arrayLength = self.wallTimeUpdaters.length;
                            for (var i = 0; i < arrayLength; i++) {
                                self.wallTimeUpdaters[i](new Date());
                            }
                        }, 1000);
                    }
                } else {
                    clonedJob.LiveWalltime = ko.computed(function () {
                        return clonedJob.Walltime;
                    });
                }

                jobsToAdd.push(clonedJob);
            }
        });

        // Add the jobs to the details array
        repgroup.details(jobsToAdd);

        // Add the new repgroup
        self.repGroups.push(repgroup);
        self.repGroupLookup[`search:${summary.name}`] = self.repGroups.length - 1;
        self.sortableRepGroups.push(repgroup);

        // Scroll to the new repgroup
        setTimeout(() => {
            const element = document.querySelector(`[data-repgroup="search:${summary.name}"]`);
            if (element) {
                element.scrollIntoView({ behavior: 'smooth', block: 'start' });
            }
        }, 100);
    };

    // Calculate aggregated summaries for search results
    self.processSearchResults = function () {
        // Group by repgroup first
        const groupedJobs = {};
        const allJobs = [];

        // Collect all jobs
        self.searchResults().forEach(job => {
            allJobs.push(job);

            // Group by repgroup
            if (!groupedJobs[job.RepGroup]) {
                groupedJobs[job.RepGroup] = [];
            }
            groupedJobs[job.RepGroup].push(job);
        });

        const summaries = [];

        // Process each repgroup
        Object.keys(groupedJobs).forEach(repgroup => {
            const jobs = groupedJobs[repgroup];
            summaries.push(self.calculateJobsSummary(jobs, repgroup));
        });

        // Add summary for all results combined
        if (summaries.length > 1) {
            summaries.push(self.calculateJobsSummary(allJobs, "all above"));
        }

        self.searchSummaries(summaries);
    };

    // Calculate summary statistics for a group of jobs
    self.calculateJobsSummary = function (jobs, groupName) {
        // State counts
        const counts = {
            complete: 0,
            running: 0,
            ready: 0,
            dependent: 0,
            lost: 0,
            delayed: 0,
            buried: 0
        };

        // Resource stats
        const memory = [];
        const disk = [];
        const walltime = [];
        const cputime = [];

        // Timeline tracking
        let earliestStart = null;
        let latestEnd = null;

        // Process each job
        jobs.forEach(job => {
            // Count states
            if (counts.hasOwnProperty(job.State)) {
                counts[job.State]++;
            }

            // Collect resource metrics
            if (job.PeakRAM > 0) memory.push(job.PeakRAM);
            if (job.PeakDisk > 0) disk.push(job.PeakDisk);
            if (job.Walltime > 0) walltime.push(job.Walltime);
            if (job.CPUtime > 0) cputime.push(job.CPUtime);

            // Track timeline
            if (job.Started && (!earliestStart || job.Started < earliestStart)) {
                earliestStart = job.Started;
            }
            if (job.Ended && (!latestEnd || job.Ended > latestEnd)) {
                latestEnd = job.Ended;
            }
        });

        // Calculate statistics
        const calcStats = (values) => {
            if (values.length === 0) return { avg: 0, stdDev: 0 };

            const avg = values.reduce((sum, val) => sum + val, 0) / values.length;
            const variance = values.reduce((sum, val) => sum + Math.pow(val - avg, 2), 0) / values.length;
            const stdDev = Math.sqrt(variance);

            return { avg, stdDev };
        };

        const memoryStats = calcStats(memory);
        const diskStats = calcStats(disk);
        const walltimeStats = calcStats(walltime);
        const cputimeStats = calcStats(cputime);

        // Calculate elapsed time
        let elapsed = 0;
        if (earliestStart && latestEnd) {
            elapsed = latestEnd - earliestStart;
        }

        // Return the summary object
        return {
            name: groupName,
            counts: counts,
            total: jobs.length,
            resources: {
                memory: memoryStats,
                disk: diskStats,
                walltime: walltimeStats,
                cputime: cputimeStats
            },
            timeline: {
                started: earliestStart,
                ended: latestEnd,
                elapsed: elapsed
            },
            hasData: memory.length > 0 || walltime.length > 0
        };
    };

    self.searchJobs = function () {
        // Clear previous results
        self.searchResults([]);
        self.searchSummaries([]);

        // Set search flags
        self.hasSearched(true);
        self.isSearchMode(true);

        // Only search if we have a RepGroup
        if (self.searchRepGroup()) {
            // Send search request
            self.ws.send(JSON.stringify({
                Request: 'details',
                RepGroup: self.searchRepGroup(),
                Search: self.searchIsSubstring()
            }));

            // Exit search mode after a timeout - assuming all results arrive within 2 seconds
            setTimeout(function () {
                self.isSearchMode(false);
                // Process the results once we've received them all
                self.processSearchResults();
            }, 2000);
        } else {
            // If no RepGroup, exit search mode immediately
            self.isSearchMode(false);
        }

        return false; // Prevent form submit
    };

    //-------------------------------------------------------------------------
    // IN-FLIGHT JOB TRACKING
    //-------------------------------------------------------------------------
    self.inflight = setupInflightTracking(self.rateLimit);

    //-------------------------------------------------------------------------
    // WEBSOCKET SETUP AND MESSAGE HANDLING
    //-------------------------------------------------------------------------
    setupWebSocket(self);

    //-------------------------------------------------------------------------
    // REPGROUP HANDLING
    //-------------------------------------------------------------------------
    self.requestRepGroup = function (formElement) {
        requestRepGroup(self);
    };

    // Functions for clicking on different progress bar types
    self.showRepgroupDelayed = function (repGroup) {
        showGroupState(self, repGroup, 'delayed');
    };

    self.showRepgroupDependent = function (repGroup) {
        showGroupState(self, repGroup, 'dependent');
    };

    self.showRepgroupReady = function (repGroup) {
        showGroupState(self, repGroup, 'ready');
    };

    self.showRepgroupRunning = function (repGroup) {
        showGroupState(self, repGroup, 'reserved'); // which includes 'running'
    };

    self.showRepgroupLost = function (repGroup) {
        showGroupState(self, repGroup, 'lost');
    };

    self.showRepgroupBuried = function (repGroup) {
        showGroupState(self, repGroup, 'buried');
    };

    self.showRepgroupComplete = function (repGroup) {
        showGroupState(self, repGroup, 'complete');
    };

    self.showGroupState = function (repGroup, state) {
        showGroupState(self, repGroup, state);
    };

    // Reset pagination when showing new group state
    self.resetPagination = function () {
        self.currentOffset = 0;
    };

    //-------------------------------------------------------------------------
    // MODAL DISPLAY HANDLERS
    //-------------------------------------------------------------------------
    // Use observable instead of observableArray since we're now passing the whole job object
    self.jobDetailsModalVisible = ko.observable(false);
    self.jobDetailsData = ko.observable();
    self.showJobDetails = function (job) {
        modalHandlers.showJobDetails(self, job);
    };

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

    //-------------------------------------------------------------------------
    // ACTION HANDLING
    //-------------------------------------------------------------------------
    self.jobToActionDetails = function (job, action, button) {
        jobToActionDetails(self, job, action, button);
    };

    self.commitAction = function (all) {
        commitAction(self, all);
    };

    //-------------------------------------------------------------------------
    // ACTION CONFIRMATION HANDLERS
    //-------------------------------------------------------------------------
    self.confirmRetry = function (job) {
        actionHandlers.confirmRetry(self, job);
    };

    self.confirmRemoveFail = function (job) {
        actionHandlers.confirmRemoveFail(self, job);
    };

    self.confirmRemoveDep = function (job) {
        actionHandlers.confirmRemoveDep(self, job);
    };

    self.confirmRemovePend = function (job) {
        actionHandlers.confirmRemovePend(self, job);
    };

    self.confirmRemoveDelay = function (job) {
        actionHandlers.confirmRemoveDelay(self, job);
    };

    self.confirmKill = function (job) {
        actionHandlers.confirmKill(self, job);
    };

    self.confirmDead = function (job) {
        actionHandlers.confirmDead(self, job);
    };

    //-------------------------------------------------------------------------
    // SERVER MANAGEMENT
    //-------------------------------------------------------------------------
    self.confirmDeadServer = function (server) {
        actionHandlers.confirmDeadServer(self, server);
    };

    //-------------------------------------------------------------------------
    // MESSAGE HANDLING
    //-------------------------------------------------------------------------
    self.dismissMessage = function (si) {
        actionHandlers.dismissMessage(self, si);
    };

    self.dismissMessages = function (si) {
        actionHandlers.dismissMessages(self);
    };

    //-------------------------------------------------------------------------
    // JOB LOADING
    //-------------------------------------------------------------------------
    self.loadMoreJobs = function (job, event) {
        loadMoreJobs(self, job, event);
    };

    self.toggleJobsList = function (repgroup) {
        if (self.selectedRepGroup() === repgroup) {
            self.selectedRepGroup('');
        } else {
            self.selectedRepGroup(repgroup);
        }
    };
}