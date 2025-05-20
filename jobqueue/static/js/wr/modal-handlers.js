/* Modal Handlers
 * Handles modals for the WR status page.
 */
import { capitalizeFirstLetter } from '/js/wr/utility.js';

/**
 * Initializes the action details object for a modal
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} job - The job object
 * @param {string} action - The action to perform
 * @param {string} button - The button text
 */
export function jobToActionDetails(viewModel, job, action, button) {
    viewModel.actionDetails.action(action);
    viewModel.actionDetails.button(button);
    viewModel.actionDetails.key(job.Key);
    viewModel.actionDetails.repGroup(job.RepGroup);
    viewModel.actionDetails.state(job.State);
    viewModel.actionDetails.exited(job.Exited);
    viewModel.actionDetails.exitCode(job.Exitcode);
    viewModel.actionDetails.failReason(job.FailReason);

    // Use TotalSimilar if available (from pagination), otherwise fallback to Similar + 1
    const count = job.TotalSimilar !== undefined ? job.TotalSimilar + 1 : job.Similar + 1;
    viewModel.actionDetails.count(count);

    // Add the utility function to the viewModel for use in templates
    viewModel.capitalizeFirstLetter = capitalizeFirstLetter;
}

/**
 * Commits an action from a modal
 * @param {StatusViewModel} viewModel - The main view model
 * @param {boolean} all - Whether to apply to all matching jobs
 */
export function commitAction(viewModel, all) {
    // Request the action
    if (all) {
        viewModel.ws.send(JSON.stringify({
            Request: viewModel.actionDetails.action(),
            RepGroup: viewModel.actionDetails.repGroup(),
            State: viewModel.actionDetails.state(),
            Exitcode: viewModel.actionDetails.exitCode(),
            FailReason: viewModel.actionDetails.failReason(),
        }));
    } else {
        viewModel.ws.send(JSON.stringify({
            Request: viewModel.actionDetails.action(),
            Key: viewModel.actionDetails.key(),
        }));
    }

    // Reset the UI
    if (viewModel.detailsOA) {
        viewModel.detailsOA([]);
    }
    viewModel.detailsRepgroup = '';
    viewModel.detailsState = '';
    viewModel.detailsOA = '';
    viewModel.actionModalVisible(false);
}

/**
 * Setup functions for each modal type
 */
export const modalHandlers = {
    showJobDetails: function (viewModel, job) {
        viewModel.jobDetailsData(job);
        viewModel.jobDetailsModalVisible(true);
    }
};
