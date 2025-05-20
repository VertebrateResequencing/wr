/* Status View Model
 * Main view model for the WR status page.
 */
import {
    getParameterByName,
    toDuration,
    toDate,
    mbIEC,
    capitalizeFirstLetter,
    setupLiveWalltime
} from '/js/wr/utility.js';
import { setupInflightTracking, createRepGroupTracker } from '/js/wr/inflight-tracking.js';
import { setupWebSocket } from '/js/wr/websocket-handler.js';
import { requestRepGroup, showGroupState, loadMoreJobs } from '/js/wr/repgroup-handler.js';
import { modalHandlers, jobToActionDetails, commitAction } from '/js/wr/modal-handlers.js';
import { actionHandlers } from '/js/wr/action-handlers.js';
import {
    createMemoryChartConfig,
    createDiskChartConfig,
    createTimeChartConfig,
    createExecutionChartConfig,
    createCombinedTimeChartConfig
} from '/js/wr/chart-config.js';

// viewmodel for displaying status
export function StatusViewModel() {
    var self = this;

    // Make utility functions available to templates
    self.mbIEC = mbIEC;
    self.toDuration = toDuration;
    self.toDate = toDate;
    self.capitalizeFirstLetter = capitalizeFirstLetter;

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
    self.searchMinimized = ko.observable(false);
    self.isSearchLoading = ko.observable(false);

    // Clear search and reset UI
    self.clearSearch = function () {
        self.searchRepGroup('');
        self.searchResults([]);
        self.searchSummaries([]);
        self.hasSearched(false);
        self.searchMinimized(false);

        // Remove any search-created RepGroups
        self.removeAllSearchRepGroups();

        return false; // Prevent form submission
    };

    // Toggle search UI minimized state
    self.toggleSearchMinimized = function () {
        self.searchMinimized(!self.searchMinimized());
        return false;
    };

    // Remove a specific search RepGroup
    self.removeSearchRepGroup = function (repgroupId) {
        let foundIndex = -1;
        for (let i = 0; i < self.repGroups.length; i++) {
            if (self.repGroups[i].id === repgroupId) {
                foundIndex = i;
                break;
            }
        }

        if (foundIndex >= 0) {
            // Remove from the sortable array first
            self.sortableRepGroups.remove(self.repGroups[foundIndex]);

            // Then remove from the array and lookup
            self.repGroups.splice(foundIndex, 1);
            delete self.repGroupLookup[repgroupId];

            // Update lookup indices
            for (const rgId in self.repGroupLookup) {
                if (self.repGroupLookup[rgId] > foundIndex) {
                    self.repGroupLookup[rgId]--;
                }
            }
        }
    };

    // Remove all search-created RepGroups
    self.removeAllSearchRepGroups = function () {
        // Identify all search RepGroups (they start with "search:")
        const searchRepGroupIndices = [];

        for (let i = 0; i < self.repGroups.length; i++) {
            if (self.repGroups[i].id.startsWith('search:')) {
                searchRepGroupIndices.push(i);
            }
        }

        // Remove them in reverse order to avoid index issues
        for (let i = searchRepGroupIndices.length - 1; i >= 0; i--) {
            const index = searchRepGroupIndices[i];
            const repgroupId = self.repGroups[index].id;

            // Remove from sortable array
            self.sortableRepGroups.remove(self.repGroups[index]);

            // Remove from array and lookup
            self.repGroups.splice(index, 1);
            delete self.repGroupLookup[repgroupId];

            // Update lookup indices for remaining groups
            for (const rgId in self.repGroupLookup) {
                if (self.repGroupLookup[rgId] > index) {
                    self.repGroupLookup[rgId]--;
                }
            }
        }
    };

    // Show jobs from a search result as a RepGroup
    self.showGroupJobs = function (summary) {
        // Store the current selected state
        const currentSelectedState = summary.selectedState();

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

        // Mark this as a search group (so we can disable click handlers)
        repgroup.isSearchGroup = true;

        // Store the selected filter state
        repgroup.selectedFilter = currentSelectedState;
        repgroup.hasCustomFilter = currentSelectedState !== 'total';

        // Update the counts based on the filter
        if (currentSelectedState === 'total') {
            // If showing all states, apply all counts
            repgroup.complete(summary.counts.complete || 0);
            repgroup.buried(summary.counts.buried || 0);
            repgroup.delayed(summary.counts.delayed || 0);
            repgroup.dependent(summary.counts.dependent || 0);
            repgroup.ready(summary.counts.ready || 0);
            repgroup.running(summary.counts.running || 0);
            repgroup.lost(summary.counts.lost || 0);
        } else {
            // If a specific state is selected, only set the count for that state
            repgroup.complete(currentSelectedState === 'complete' ? summary.counts.complete || 0 : 0);
            repgroup.buried(currentSelectedState === 'buried' ? summary.counts.buried || 0 : 0);
            repgroup.delayed(currentSelectedState === 'delayed' ? summary.counts.delayed || 0 : 0);
            repgroup.dependent(currentSelectedState === 'dependent' ? summary.counts.dependent || 0 : 0);
            repgroup.ready(currentSelectedState === 'ready' ? summary.counts.ready || 0 : 0);
            repgroup.running(currentSelectedState === 'running' ? summary.counts.running || 0 : 0);
            repgroup.lost(currentSelectedState === 'lost' ? summary.counts.lost || 0 : 0);
        }

        // Always set deleted to 0 as it's not tracked in search results
        repgroup.deleted(0);

        // Add the jobs to the details
        const jobsToAdd = [];

        // Filter jobs based on the summary name and selected state
        self.searchResults().forEach(job => {
            if ((summary.name === job.RepGroup) || (summary.name === "all above")) {
                // Apply state filter if not showing all jobs
                if (currentSelectedState !== 'total' && job.State !== currentSelectedState) {
                    return; // Skip jobs that don't match the selected state
                }

                // Clone the job to avoid reference issues
                const clonedJob = JSON.parse(JSON.stringify(job));

                // Set up LiveWalltime for the job
                setupLiveWalltime(clonedJob, clonedJob.Walltime, self);

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

            // Handle the case with only one value
            if (values.length === 1) return { avg, stdDev: 0 };

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
            hasData: memory.length > 0 || walltime.length > 0,
            selectedState: ko.observable('total') // Default selection is 'total'
        };
    };

    // Calculate statistics for jobs filtered by state
    self.getFilteredJobStats = function (summary, state) {
        // For the total view, we still want to only use data from completed/buried jobs for resource statistics
        const useCompletedOnly = (state === 'total');

        // Get the filtered jobs based on the selected state
        const filteredJobs = self.searchResults().filter(job =>
            (job.RepGroup === summary.name || summary.name === "all above") &&
            (state === 'total' || job.State === state)
        );

        // No jobs match the filter
        if (filteredJobs.length === 0) {
            return {
                resources: {
                    memory: { avg: 0, stdDev: 0 }, disk: { avg: 0, stdDev: 0 },
                    walltime: { avg: 0, stdDev: 0 }, cputime: { avg: 0, stdDev: 0 }
                },
                timeline: { started: null, ended: null, elapsed: 0 },
                hasData: false
            };
        }

        // For resource stats, we only want to use data from completed or buried jobs
        // If we're in 'total' view, filter to only completed/buried jobs for stats
        const statsJobs = useCompletedOnly
            ? filteredJobs.filter(job => job.State === 'complete' || job.State === 'buried')
            : filteredJobs;

        // If no complete/buried jobs available for stats, return empty stats
        if (statsJobs.length === 0) {
            return {
                resources: {
                    memory: { avg: 0, stdDev: 0 }, disk: { avg: 0, stdDev: 0 },
                    walltime: { avg: 0, stdDev: 0 }, cputime: { avg: 0, stdDev: 0 }
                },
                timeline: { started: null, ended: null, elapsed: 0 },
                hasData: false
            };
        }

        // Calculate stats for filtered and completed/buried jobs
        const calcStats = (values) => {
            if (values.length === 0) return { avg: 0, stdDev: 0 };

            const avg = values.reduce((sum, val) => sum + val, 0) / values.length;

            // Handle the case with only one value - standard deviation is undefined
            if (values.length === 1) return { avg, stdDev: 0 };

            const variance = values.reduce((sum, val) => sum + Math.pow(val - avg, 2), 0) / values.length;
            const stdDev = Math.sqrt(variance);
            return { avg, stdDev };
        };

        // Extract resource metrics from statsJobs (completed/buried only)
        const memory = statsJobs.filter(job => job.PeakRAM > 0).map(job => job.PeakRAM);
        const disk = statsJobs.filter(job => job.PeakDisk > 0).map(job => job.PeakDisk);
        const walltime = statsJobs.filter(job => job.Walltime > 0).map(job => job.Walltime);
        const cputime = statsJobs.filter(job => job.CPUtime > 0).map(job => job.CPUtime);

        // Track timeline from statsJobs (completed/buried only)
        let earliestStart = null;
        let latestEnd = null;

        statsJobs.forEach(job => {
            if (job.Started && (!earliestStart || job.Started < earliestStart)) {
                earliestStart = job.Started;
            }
            if (job.Ended && (!latestEnd || job.Ended > latestEnd)) {
                latestEnd = job.Ended;
            }
        });

        // Calculate elapsed time
        let elapsed = 0;
        if (earliestStart && latestEnd) {
            elapsed = latestEnd - earliestStart;
        }

        return {
            resources: {
                memory: calcStats(memory),
                disk: calcStats(disk),
                walltime: calcStats(walltime),
                cputime: calcStats(cputime)
            },
            timeline: {
                started: earliestStart,
                ended: latestEnd,
                elapsed: elapsed
            },
            hasData: memory.length > 0 || walltime.length > 0
        };
    };

    // Set the selected state for a summary and update stats
    self.setSelectedState = function (summary, state) {
        // Update the selected state
        summary.selectedState(state);
        return false; // Prevent default
    };

    self.searchJobs = function () {
        // Set loading state
        self.isSearchLoading(true);

        // Close any open details sections first
        if (self.detailsOA) {
            // Unsubscribe from current updates
            self.ws.send(JSON.stringify({
                Request: "unsubscribe"
            }));

            if (self.wallTimeUpdater) {
                self.wallTimeUpdaters = new Array();
                window.clearInterval(self.wallTimeUpdater);
                self.wallTimeUpdater = '';
            }

            // Clear current details
            self.detailsOA([]);
            self.detailsRepgroup = '';
            self.detailsState = '';
            self.detailsOA = '';
        }

        // Clear previous results
        self.searchResults([]);
        self.searchSummaries([]);
        self.searchMinimized(false);

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
                self.isSearchLoading(false); // End loading state

                // Unsubscribe from the automatically subscribed jobs
                self.ws.send(JSON.stringify({
                    Request: "unsubscribe"
                }));

                // Process the results once we've received them all
                self.processSearchResults();
            }, 2000);
        } else {
            // If no RepGroup, exit search mode immediately
            self.isSearchMode(false);
            self.isSearchLoading(false); // End loading state
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

    // Function to check if this is a search-created repgroup before handling progress bar clicks
    self.showRepgroupDelayed = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return; // Don't handle clicks for search-created groups
        showGroupState(self, repGroup, 'delayed');
    };

    self.showRepgroupDependent = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
        showGroupState(self, repGroup, 'dependent');
    };

    self.showRepgroupReady = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
        showGroupState(self, repGroup, 'ready');
    };

    self.showRepgroupRunning = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
        showGroupState(self, repGroup, 'reserved'); // which includes 'running'
    };

    self.showRepgroupLost = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
        showGroupState(self, repGroup, 'lost');
    };

    self.showRepgroupBuried = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
        showGroupState(self, repGroup, 'buried');
    };

    self.showRepgroupComplete = function (repGroup) {
        if (repGroup.id.startsWith('search:')) return;
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

    // Function to show resource visualization charts
    self.showResourceChart = function (resourceType, summary, statsData) {
        // Get filtered jobs based on the selected state
        const selectedState = summary.selectedState();
        const jobsToAnalyze = self.searchResults().filter(job =>
            (job.RepGroup === summary.name || summary.name === "all above") &&
            (selectedState === 'total' || job.State === selectedState)
        );

        // Filter to completed/buried jobs for stats if in total view
        const statsJobs = selectedState === 'total'
            ? jobsToAnalyze.filter(job => job.State === 'complete' || job.State === 'buried')
            : jobsToAnalyze;

        if (statsJobs.length === 0) {
            alert('No data available for visualization');
            return;
        }

        let config = null;
        let statsHtml = '';

        try {
            // Choose config based on resource type
            switch (resourceType) {
                case 'memory':
                    // Get memory values (must have a value > 0)
                    const memoryValues = statsJobs
                        .filter(job => job.PeakRAM > 0)
                        .map(job => job.PeakRAM);

                    if (memoryValues.length === 0) {
                        alert('No memory usage data available');
                        return;
                    }

                    config = createMemoryChartConfig(memoryValues);
                    statsHtml = generateStatsHtml(memoryValues, 'Memory', value => mbIEC(value));
                    break;

                case 'disk':
                    const diskValues = statsJobs
                        .filter(job => job.PeakDisk > 0)
                        .map(job => job.PeakDisk);

                    if (diskValues.length === 0) {
                        alert('No disk usage data available');
                        return;
                    }

                    config = createDiskChartConfig(diskValues);
                    statsHtml = generateStatsHtml(diskValues, 'Disk', value => mbIEC(value));
                    break;

                case 'time':
                    // Get walltime and cputime values (must have a value > 0)
                    const walltimeValues = statsJobs
                        .filter(job => job.Walltime > 0)
                        .map(job => job.Walltime);

                    const cputimeValues = statsJobs
                        .filter(job => job.CPUtime > 0)
                        .map(job => job.CPUtime);

                    if (walltimeValues.length === 0 && cputimeValues.length === 0) {
                        alert('No time data available');
                        return;
                    }

                    config = createCombinedTimeChartConfig(walltimeValues, cputimeValues);

                    // Create combined stats HTML for both metrics
                    statsHtml = '<div class="row"><div class="col-md-6">';
                    statsHtml += '<h5>Wall Time</h5>';
                    statsHtml += generateStatsHtml(walltimeValues, 'Wall Time', value => toDuration(value));
                    statsHtml += '</div><div class="col-md-6">';
                    statsHtml += '<h5>CPU Time</h5>';
                    statsHtml += generateStatsHtml(cputimeValues, 'CPU Time', value => toDuration(value));
                    statsHtml += '</div></div>';
                    break;

                case 'walltime':
                case 'cputime':
                    // Keeping these for backward compatibility, but they'll use the bar chart
                    const isWallTime = resourceType === 'walltime';
                    const timeValues = statsJobs
                        .filter(job => isWallTime ? job.Walltime > 0 : job.CPUtime > 0)
                        .map(job => isWallTime ? job.Walltime : job.CPUtime);

                    config = createTimeChartConfig(timeValues, resourceType);
                    statsHtml = generateStatsHtml(timeValues,
                        isWallTime ? 'Wall Time' : 'CPU Time',
                        value => toDuration(value));
                    break;

                case 'execution':
                    config = createExecutionChartConfig(statsJobs);
                    if (config && config.statsHtml) {
                        statsHtml = config.statsHtml;
                    }
                    break;
            }

            if (!config) {
                alert('Could not create visualization: insufficient data');
                return;
            }

            // Set the stats HTML
            document.getElementById('chartStats').innerHTML = statsHtml;

            // Get the canvas and clear existing chart
            const ctx = document.getElementById('resourceChart').getContext('2d');
            if (window.resourceChart && typeof window.resourceChart.destroy === 'function') {
                window.resourceChart.destroy();
            }

            // Create a new chart
            window.resourceChart = new Chart(ctx, {
                type: config.type,
                data: config.data,
                options: config.options
            });

            // Set modal title and show it
            document.getElementById('resourceChartModalLabel').textContent = config.title;
            $('#resourceChartModal').modal('show');

        } catch (error) {
            console.error("Error creating chart:", error);
            // Show an error message in the modal
            document.getElementById('chartStats').innerHTML =
                `<div class="alert alert-danger">Error creating chart: ${error.message}.</div>` +
                statsHtml;

            // Still show the modal with the error
            $('#resourceChartModal').modal('show');
        }
    };

    // Helper function to generate statistics HTML
    function generateStatsHtml(data, label, formatter) {
        if (data.length === 0) return `<div>No data available</div>`;

        // Calculate basic statistics
        data.sort((a, b) => a - b);
        const min = data[0];
        const max = data[data.length - 1];
        const sum = data.reduce((a, b) => a + b, 0);
        const mean = sum / data.length;

        // Calculate median
        const mid = Math.floor(data.length / 2);
        const median = data.length % 2 === 0
            ? (data[mid - 1] + data[mid]) / 2
            : data[mid];

        // Calculate standard deviation
        const variance = data.reduce((acc, val) => acc + Math.pow(val - mean, 2), 0) / data.length;
        const stdDev = Math.sqrt(variance);

        // Calculate quartiles
        const q1Index = Math.floor(data.length * 0.25);
        const q3Index = Math.floor(data.length * 0.75);
        const q1 = data[q1Index];
        const q3 = data[q3Index];

        return `
    <div class="stat-item"><span class="stat-label">Count:</span> ${data.length}</div>
    <div class="stat-item"><span class="stat-label">Min:</span> ${formatter(min)}</div>
    <div class="stat-item"><span class="stat-label">Max:</span> ${formatter(max)}</div>
    <div class="stat-item"><span class="stat-label">Mean:</span> ${formatter(mean)}</div>
    <div class="stat-item"><span class="stat-label">Median:</span> ${formatter(median)}</div>
    <div class="stat-item"><span class="stat-label">Std Dev:</span> ${formatter(stdDev)}</div>
    <div class="stat-item"><span class="stat-label">Q1:</span> ${formatter(q1)}</div>
        <div class="stat-item"><span class="stat-label">Q3:</span> ${formatter(q3)}</div>
    `;
    }
}