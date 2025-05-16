/* Utility Functions
 * Helper functions for the WR status page.
 */

/**
 * Removes a bad server from the badservers array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} id - The ID of the server to remove
 */
export function removeBadServer(viewModel, id) {
    viewModel.badservers.remove(function (server) {
        return server.ID == id;
    });
}

/**
 * Removes a message from the messages array
 * @param {StatusViewModel} viewModel - The main view model
 * @param {string} msg - The message to remove
 */
export function removeMessage(viewModel, msg) {
    viewModel.messages.remove(function (schedIssue) {
        return schedIssue.Msg == msg;
    });
}

/**
 * Gets a URL parameter by name
 * @param {string} name - The name of the parameter
 * @returns {string} The value of the parameter
 */
export function getParameterByName(name) {
    var match = RegExp('[?&]' + name + '=([^&]*)').exec(window.location.search);
    return match && decodeURIComponent(match[1].replace(/\+/g, ' '));
}
