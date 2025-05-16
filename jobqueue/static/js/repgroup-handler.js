/* RepGroup Handler
 * Handles RepGroup-related functionality for the WR status page.
 */

/**
 * Handles requesting a specific RepGroup
 * @param {StatusViewModel} viewModel - The main view model
 */
export function requestRepGroup(viewModel) {
    console.log("requesting rep group " + viewModel.repGroup());
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

    // Increase the limit by a reasonable number
    viewModel.currentLimit += 5;

    // Request more jobs with the increased limit
    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: viewModel.detailsRepgroup,
        State: viewModel.detailsState,
        Limit: viewModel.currentLimit
    }));
}
