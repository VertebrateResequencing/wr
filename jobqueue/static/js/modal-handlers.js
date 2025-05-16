/* Modal Handlers
 * Handles modals for the WR status page.
 */

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
    viewModel.actionDetails.exitCode(job.Exited);
    viewModel.actionDetails.exitCode(job.Exitcode);
    viewModel.actionDetails.failReason(job.FailReason);
    viewModel.actionDetails.count(job.Similar + 1);
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
    // LimitGroups modal
    showLimitGroups: function (viewModel, job) {
        viewModel.lgVars(job.LimitGroups);
        viewModel.lgModalVisible(true);
    },

    // DepGroups modal
    showDepGroups: function (viewModel, job) {
        viewModel.dgVars(job.DepGroups);
        viewModel.dgModalVisible(true);
    },

    // Dependencies modal
    showDependencies: function (viewModel, job) {
        viewModel.depVars(job.Dependencies);
        viewModel.depModalVisible(true);
    },

    // Behaviours modal
    showBehaviours: function (viewModel, job) {
        viewModel.behVars([job.Behaviours]);
        viewModel.behModalVisible(true);
    },

    // Other modal
    showOther: function (viewModel, job) {
        viewModel.otherVars(job.OtherRequests);
        viewModel.otherModalVisible(true);
    },

    // InternalID modal
    showInternalID: function (viewModel, job) {
        viewModel.internalVars([job.Key]);
        viewModel.internalModalVisible(true);
    },

    // Env modal
    showEnv: function (viewModel, job) {
        viewModel.envVars(job.Env);
        viewModel.envModalVisible(true);
    }
};
