/* Action Handlers
 * Handles job actions for the WR status page.
 */
import { removeBadServer, removeMessage } from '/js/utility.js';
import { jobToActionDetails } from '/js/modal-handlers.js';

/**
 * Action handler functions for jobs
 */
export const actionHandlers = {
    // Server actions
    confirmDeadServer: function (viewModel, server) {
        viewModel.ws.send(JSON.stringify({
            Request: 'confirmBadServer',
            ServerID: server.ID
        }));
        removeBadServer(viewModel, server.ID);
    },

    // Message actions
    dismissMessage: function (viewModel, message) {
        viewModel.ws.send(JSON.stringify({
            Request: 'dismissMsg',
            Msg: message.Msg
        }));
        removeMessage(viewModel, message.Msg);
    },

    dismissMessages: function (viewModel) {
        viewModel.ws.send(JSON.stringify({
            Request: 'dismissMsgs'
        }));
        viewModel.messages([]);
    },

    // Job actions with confirmation modals
    confirmRetry: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'retry', 'retry');
        viewModel.actionModalHeader('Retry Buried Commands');
        viewModel.actionModalVisible(true);
    },

    confirmRemoveFail: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'remove', 'remove');
        viewModel.actionModalHeader('Remove Failed Commands');
        viewModel.actionModalVisible(true);
    },

    confirmRemoveDep: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'remove', 'remove');
        viewModel.actionModalHeader('Remove Dependent Commands');
        viewModel.actionModalVisible(true);
    },

    confirmRemovePend: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'remove', 'remove');
        viewModel.actionModalHeader('Remove Pending Commands');
        viewModel.actionModalVisible(true);
    },

    confirmRemoveDelay: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'remove', 'remove');
        viewModel.actionModalHeader('Remove Delayed Commands');
        viewModel.actionModalVisible(true);
    },

    confirmKill: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'kill', 'kill');
        viewModel.actionModalHeader('Kill Running Commands');
        viewModel.actionModalVisible(true);
    },

    confirmDead: function (viewModel, job) {
        jobToActionDetails(viewModel, job, 'kill', 'confirm');
        viewModel.actionModalHeader('Confirm Commands are Dead');
        viewModel.actionModalVisible(true);
    }
};
