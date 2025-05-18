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
        return;
    }

    viewModel.detailsRepgroup = repGroup.id;
    viewModel.detailsState = state;
    viewModel.detailsOA = repGroup.details;
    viewModel.currentLimit = 1; // Start with a limit of 1
    viewModel.currentOffset = 0; // Reset offset for new details view

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

    const heightAdjustment = 200;
    const wellContainer = panelFooter.closest('.well');
    const scrollToY = wellContainer ?
        wellContainer.offsetTop + wellContainer.offsetHeight - heightAdjustment :
        document.body.scrollHeight - heightAdjustment;

    // For the first click, start at offset 1 (since we already have offset 0 with limit 1)
    // For subsequent clicks, increment by 5
    if (viewModel.currentOffset === 0) {
        viewModel.currentOffset = 1;
    } else {
        viewModel.currentOffset += 5;
    }

    // Request more jobs with pagination offset and including all job details
    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: viewModel.detailsRepgroup,
        State: viewModel.detailsState,
        Limit: 5, // Constant limit of 5
        Offset: viewModel.currentOffset,
        Exitcode: job.Exitcode,
        FailReason: job.FailReason
    }));

    // Wait a short moment for the request to be sent, then scroll to the calculated position
    setTimeout(() => {
        window.scrollTo({
            top: scrollToY,
            behavior: 'smooth'
        });
    }, 100);
}
