import { getParameterByName } from '/js/utility.js';
import { setupInflightTracking, createRepGroupTracker } from './inflight-tracking.js';
import { setupWebSocket } from '/js/websocket-handler.js';
import { requestRepGroup, showGroupState, loadMoreJobs } from './repgroup-handler.js';
import { modalHandlers } from '/js/modal-handlers.js';
import { actionHandlers } from '/js/action-handlers.js';
import { StatusViewModel } from '/js/status-viewmodel.js';

// Helper function to initialize percentage calculations
window.percentScaler = function (arr, max) {
    return arr;
};

window.percentRounder = function (arr, precision) {
    return arr;
};

// Extend String prototype with utility method
String.prototype.capitalizeFirstLetter = function () {
    return this.charAt(0).toUpperCase() + this.slice(1);
};

// Initialize the application
document.addEventListener('DOMContentLoaded', () => {
    ko.options.deferUpdates = true;
    const svm = new StatusViewModel();
    ko.applyBindings(svm, document.getElementById('status'));
});
