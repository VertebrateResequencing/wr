/* RepGroup Handler
 * Handles RepGroup-related functionality for the WR status page.
 */

/**
 * Handles requesting a specific RepGroup
 * @param {StatusViewModel} viewModel - The main view model
 */
export function requestRepGroup(viewModel) {
    viewModel.ws.send(JSON.stringify({
        Request: 'search',
        RepGroup: viewModel.repGroup()
    }));
}

/**
 * Shows jobs in a specific state for a RepGroup
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} repGroup - The RepGroup object
 * @param {string} state - The state to show
 */
export function showGroupState(viewModel, repGroup, state) {
    if (viewModel.detailsOA) {
        if (viewModel.wallTimeUpdater) {
            viewModel.wallTimeUpdaters = new Array();
            window.clearInterval(viewModel.wallTimeUpdater);
            viewModel.wallTimeUpdater = '';
        }
        viewModel.detailsOA([]);
    }

    if (repGroup.id == viewModel.detailsRepgroup && state == viewModel.detailsState) {
        // Stop showing details
        repGroup.details([]);
        viewModel.detailsRepgroup = '';
        viewModel.detailsState = '';
        viewModel.detailsOA = '';
        viewModel.currentLimit = 1; // Reset limit when closing details
        return;
    }

    viewModel.detailsRepgroup = repGroup.id;
    viewModel.detailsState = state;
    viewModel.detailsOA = repGroup.details;
    viewModel.currentLimit = 1; // Start with a limit of 1

    // Clean up any existing job loading state
    cleanupJobLoading(viewModel);

    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: repGroup.id,
        State: state,
        Limit: viewModel.currentLimit
    }));
}

/**
 * Clean up job loading state
 * @param {StatusViewModel} viewModel - The main view model
 */
function cleanupJobLoading(viewModel) {
    viewModel.pendingBatch = []; // Temporary storage for jobs from each server response
    viewModel.jobQueue = [];     // Storage for validated jobs ready to display
    viewModel.seenJobKeys = new Set();
    viewModel.loadingSourceJob = null;
    viewModel.isLoadingBatch = false;
    viewModel.totalSimilarJobs = 0;
    viewModel.jobsLoaded = 0;
}

/**
 * Loads more jobs for the current details view
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} job - The job object that triggered the action
 * @param {Event} event - The click event
 */
export function loadMoreJobs(viewModel, job, event) {
    // Find the parent panel-footer element containing the button
    const clickedElement = event.target;
    const panelFooter = clickedElement.closest('.panel-footer');

    if (panelFooter) {
        // Find the load-more section
        const loadMoreSection = panelFooter.querySelector('.load-more-section');
        if (loadMoreSection) {
            // Hide the entire load-more-section
            loadMoreSection.style.display = 'none';
        }
    }

    // Don't start a new request if we're already loading
    if (viewModel.isLoadingBatch) {
        return;
    }

    // Record the current number of jobs displayed
    viewModel.jobsLoaded = viewModel.detailsOA().length;

    // Get the total number of similar jobs from the initial job
    viewModel.totalSimilarJobs = viewModel.jobsLoaded + job.Similar;

    // Calculate how many more we can load
    const remainingJobs = viewModel.totalSimilarJobs - viewModel.jobsLoaded;

    // Don't do anything if there are no more jobs to load
    if (remainingJobs <= 0) {
        return;
    }

    // Initialize the job loading process
    initializeJobLoading(viewModel, job);

    // Make the request
    requestJobBatch(viewModel);
}

/**
 * Initialize job loading process
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} job - The job that triggered loading
 */
function initializeJobLoading(viewModel, job) {
    // Mark that we're loading a batch
    viewModel.isLoadingBatch = true;

    // Store reference to the source job (for matching criteria)
    viewModel.loadingSourceJob = job;

    // Initialize or clear the set of seen job keys
    if (!viewModel.seenJobKeys) {
        viewModel.seenJobKeys = new Set();
    }

    // Add all currently displayed jobs to the set
    const currentJobs = viewModel.detailsOA();
    currentJobs.forEach(j => viewModel.seenJobKeys.add(j.Key));

    // Initialize storage arrays
    viewModel.pendingBatch = []; // Temporary storage for jobs from each server response
    viewModel.jobQueue = [];     // Storage for validated jobs ready to display

    // We want to display at most 5 new jobs per batch
    viewModel.displayLimit = Math.min(5, viewModel.totalSimilarJobs - viewModel.jobsLoaded);

    // Request parameters
    viewModel.batchSize = 20;        // Request 20 jobs at a time
    viewModel.maxAttempts = 5;       // Maximum number of attempts to find jobs
    viewModel.currentAttempt = 0;    // Current attempt counter
}

/**
 * Request a batch of jobs from the server
 * @param {StatusViewModel} viewModel - The main view model
 */
function requestJobBatch(viewModel) {
    // Clear pending batch for new server response
    viewModel.pendingBatch = [];

    // Increment attempt counter
    viewModel.currentAttempt++;

    // If we've reached max attempts or found enough jobs, finish up
    if (viewModel.currentAttempt > viewModel.maxAttempts || viewModel.jobQueue.length >= viewModel.displayLimit) {
        displayJobBatch(viewModel);
        return;
    }

    // Send request for a batch of jobs
    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: viewModel.detailsRepgroup,
        State: viewModel.detailsState,
        Limit: viewModel.batchSize
    }));

    // Wait a bit for the server to respond before checking if we need more
    setTimeout(() => checkAndRequestMore(viewModel), 500);
}

/**
 * Check if we need more jobs and request another batch if needed
 * @param {StatusViewModel} viewModel - The main view model
 */
function checkAndRequestMore(viewModel) {
    // Only continue if we're still in batch loading mode
    if (!viewModel.isLoadingBatch) {
        return;
    }

    // Process any pending batch first
    processPendingBatch(viewModel);

    // If we have enough jobs or hit max attempts, display what we have
    if (viewModel.jobQueue.length >= viewModel.displayLimit || viewModel.currentAttempt >= viewModel.maxAttempts) {
        displayJobBatch(viewModel);
    } else {
        // Otherwise, request another batch
        requestJobBatch(viewModel);
    }
}

/**
 * Process any pending batch of jobs and move matching jobs to the queue
 * @param {StatusViewModel} viewModel - The main view model
 */
function processPendingBatch(viewModel) {
    if (!viewModel.pendingBatch || viewModel.pendingBatch.length === 0) {
        return;
    }

    // Filter jobs that match our criteria
    for (const job of viewModel.pendingBatch) {
        // Skip if we already have enough jobs
        if (viewModel.jobQueue.length >= viewModel.displayLimit) {
            break;
        }

        // Check if this is a new job we haven't seen before
        if (!viewModel.seenJobKeys.has(job.Key)) {
            // Mark that we've seen this job
            viewModel.seenJobKeys.add(job.Key);

            // Check if this job matches the criteria of the source job
            if (job.Exitcode === viewModel.loadingSourceJob.Exitcode &&
                job.FailReason === viewModel.loadingSourceJob.FailReason) {

                // Add to the job queue for display
                viewModel.jobQueue.push(job);
            }
        }
    }

    // Clear the pending batch
    viewModel.pendingBatch = [];
}

/**
 * Process a job received from the server during batch loading
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} job - The job received from the server
 * @returns {boolean} True if the job was processed as part of batch loading
 */
export function processJobBatch(viewModel, job) {
    // If we're not in batch loading mode, return false
    if (!viewModel.isLoadingBatch) {
        return false;
    }

    // Add job to the pending batch for later processing
    viewModel.pendingBatch.push(job);
    return true;
}

/**
 * Display the batch of jobs we've collected in the queue
 * @param {StatusViewModel} viewModel - The main view model
 */
function displayJobBatch(viewModel) {
    // Don't do anything if we're not in batch loading mode
    if (!viewModel.isLoadingBatch) {
        return;
    }

    // Process any pending jobs first
    processPendingBatch(viewModel);

    // Get the jobs to display (at most displayLimit)
    const jobsToDisplay = viewModel.jobQueue.splice(0, viewModel.displayLimit);

    // If we have jobs to display, add them to the view
    if (jobsToDisplay.length > 0) {
        // Calculate the remaining jobs after this batch
        const remaining = viewModel.totalSimilarJobs - (viewModel.jobsLoaded + jobsToDisplay.length);

        // Process each job
        jobsToDisplay.forEach((job, index) => {
            // If this is the last job in the batch, set its Similar count to show
            // how many more jobs are available
            if (index === jobsToDisplay.length - 1) {
                job.Similar = Math.max(0, remaining);
            } else {
                // Other jobs shouldn't show the "Load more" button
                job.Similar = 0;
            }

            // Add to the observable array
            viewModel.detailsOA.push(job);
        });

        // Update the count of loaded jobs
        viewModel.jobsLoaded += jobsToDisplay.length;
    }

    // Reset the batch loading state
    viewModel.isLoadingBatch = false;
    viewModel.pendingBatch = [];
}
