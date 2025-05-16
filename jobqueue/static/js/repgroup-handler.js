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
 */
export function loadMoreJobs(viewModel) {
    // Increase the limit by a reasonable number
    viewModel.currentLimit += 5;

    // Clear the current details to avoid duplicates
    if (viewModel.detailsOA) {
        viewModel.detailsOA([]);
    }

    // Request more jobs with the increased limit
    viewModel.ws.send(JSON.stringify({
        Request: 'details',
        RepGroup: viewModel.detailsRepgroup,
        State: viewModel.detailsState,
        Limit: viewModel.currentLimit
    }));
}
