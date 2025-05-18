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
        viewModel.currentOffset = 0; // Reset offset when closing details
        viewModel.offsetMap = {};    // Clear the offset map
        viewModel.newJobsInfo = {};  // Clear the batch tracking map
        return;
    }

    viewModel.detailsRepgroup = repGroup.id;
    viewModel.detailsState = state;
    viewModel.detailsOA = repGroup.details;
    viewModel.currentLimit = 1; // Start with a limit of 1
    viewModel.currentOffset = 0; // Reset offset for new details view
    viewModel.offsetMap = {};    // Clear the offset map when showing a new group
    viewModel.newJobsInfo = {};  // Clear the batch tracking map

    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: repGroup.id,
        State: state,
        Limit: viewModel.currentLimit
    }));
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

    // Find the parent container that holds all the job panels for this RepGroup
    const container = document.querySelector(`[data-repgroup="${viewModel.detailsRepgroup}"]`);
    if (container) {
        // Create a divider element with a temporary message
        const divider = document.createElement('div');
        divider.className = 'jobs-divider';
        divider.id = 'more-jobs-divider'; // Add an ID so we can find it later
        divider.innerHTML = `<span class="jobs-divider-label">
                            Loading more jobs that exited ${job.Exitcode}
                            ${job.FailReason ? ` because "${job.FailReason}"` : ''}...
                        </span>`;

        // Find all existing job panels
        const jobPanels = container.querySelectorAll('.panel');
        if (jobPanels.length > 0) {
            // Add the divider after the last job panel
            const lastJob = jobPanels[jobPanels.length - 1];
            lastJob.after(divider);
        }

        // Calculate scroll position to be just above the divider
        const dividerRect = divider.getBoundingClientRect();
        const scrollOffset = 20; // Show some context above the divider
        const scrollToY = window.scrollY + dividerRect.top - scrollOffset;

        // Create a key for this exitcode+reason combination
        const exitReasonKey = `${job.Exitcode}:${job.FailReason || ''}`;

        // Store batch information in the map
        if (!viewModel.newJobsInfo[exitReasonKey]) {
            // First time seeing this exitcode+reason combination
            viewModel.newJobsInfo[exitReasonKey] = {
                exitCode: job.Exitcode,
                failReason: job.FailReason,
                totalSimilar: job.Similar, // Store the original Similar count
                batchCount: 0,
                dividerElement: divider
            };
        } else {
            // Update the existing entry with the new divider
            viewModel.newJobsInfo[exitReasonKey].dividerElement = divider;
            viewModel.newJobsInfo[exitReasonKey].batchCount = 0;
            // Don't update totalSimilar - keep the original value
        }

        // Get the current offset for this exitcode+reason, or initialize to 1
        if (!viewModel.offsetMap[exitReasonKey]) {
            // Count how many jobs with this exitcode+reason are already displayed
            let existingCount = 0;

            // We'll check through each existing job to count ones with matching exitcode+reason
            const jobs = viewModel.detailsOA();
            for (let i = 0; i < jobs.length; i++) {
                if (jobs[i].Exitcode === job.Exitcode &&
                    (jobs[i].FailReason || '') === (job.FailReason || '')) {
                    existingCount++;
                }
            }

            // If we have just one job displayed (the original), start at offset 1
            // Otherwise, our offset should be the count of existing jobs
            viewModel.offsetMap[exitReasonKey] = Math.max(1, existingCount);
        } else {
            // Increment the existing offset by 5 (or whatever the batch size is)
            viewModel.offsetMap[exitReasonKey] += 5;
        }

        // Request more jobs with pagination offset and including all job details
        viewModel.ws.send(JSON.stringify({
            Request: 'details',
            RepGroup: viewModel.detailsRepgroup,
            State: viewModel.detailsState,
            Limit: 5, // Constant limit of 5
            Offset: viewModel.offsetMap[exitReasonKey],
            Exitcode: job.Exitcode,
            FailReason: job.FailReason
        }));

        // Wait a short moment for the request to be sent, then scroll to the divider
        setTimeout(() => {
            window.scrollTo({
                top: scrollToY,
                behavior: 'smooth'
            });
        }, 100);
    }
}
