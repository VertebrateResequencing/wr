/* Status View Model
 * Main view model for the WR status page.
 */
import { getParameterByName, removeBadServer, removeMessage } from '/js/utility.js';
import { setupInflightTracking } from '/js/inflight-tracking.js';
import { setupWebSocket } from '/js/websocket-handler.js';
import { requestRepGroup, showGroupState, loadMoreJobs } from '/js/repgroup-handler.js';
import { modalHandlers, jobToActionDetails, commitAction } from '/js/modal-handlers.js';
import { actionHandlers } from '/js/action-handlers.js';

// viewmodel for displaying status
export function StatusViewModel() {
    var self = this;

    //-------------------------------------------------------------------------
    // PROPERTIES AND BASIC OBSERVABLES
    //-------------------------------------------------------------------------
    self.token = getParameterByName("token");
    self.aquiringstatus = ko.observableArray();
    self.statuserror = ko.observableArray();
    self.badservers = ko.observableArray();
    self.messages = ko.observableArray();
    self.repGroup = ko.observable();
    self.detailsRepgroup = '';
    self.detailsState = '';
    self.detailsOA;
    self.wallTimeUpdater;
    self.wallTimeUpdaters = new Array();
    self.rateLimit = 350;
    self.currentLimit = 1;
    self.currentOffset = 0; // Add offset tracking for pagination
    self.newJobsInfo = null; // Tracks info about newly loaded jobs
    self.lastDivider = null; // References the divider element
    self.repGroups = [];
    self.repGroupLookup = {};
    self.sortableRepGroups = ko.observableArray();
    self.ignore = {};

    //-------------------------------------------------------------------------
    // UTILITY FUNCTIONS
    //-------------------------------------------------------------------------
    self.removeBadServer = function (id) {
        removeBadServer(self, id);
    };

    self.removeMessage = function (msg) {
        removeMessage(self, msg);
    };

    //-------------------------------------------------------------------------
    // IN-FLIGHT JOB TRACKING
    //-------------------------------------------------------------------------
    self.inflight = setupInflightTracking(self.rateLimit);

    //-------------------------------------------------------------------------
    // WEBSOCKET SETUP AND MESSAGE HANDLING
    //-------------------------------------------------------------------------
    setupWebSocket(self);

    //-------------------------------------------------------------------------
    // REPGROUP HANDLING
    //-------------------------------------------------------------------------
    self.requestRepGroup = function (formElement) {
        requestRepGroup(self);
    };

    // Functions for clicking on different progress bar types
    self.showRepgroupDelayed = function (repGroup) {
        showGroupState(self, repGroup, 'delayed');
    };

    self.showRepgroupDependent = function (repGroup) {
        showGroupState(self, repGroup, 'dependent');
    };

    self.showRepgroupReady = function (repGroup) {
        showGroupState(self, repGroup, 'ready');
    };

    self.showRepgroupRunning = function (repGroup) {
        showGroupState(self, repGroup, 'reserved'); // which includes 'running'
    };

    self.showRepgroupLost = function (repGroup) {
        showGroupState(self, repGroup, 'lost');
    };

    self.showRepgroupBuried = function (repGroup) {
        showGroupState(self, repGroup, 'buried');
    };

    self.showRepgroupComplete = function (repGroup) {
        showGroupState(self, repGroup, 'complete');
    };

    self.showGroupState = function (repGroup, state) {
        showGroupState(self, repGroup, state);
    };

    // Reset pagination when showing new group state
    self.resetPagination = function () {
        self.currentOffset = 0;
    };

    //-------------------------------------------------------------------------
    // MODAL DISPLAY HANDLERS
    //-------------------------------------------------------------------------
    // Use observable instead of observableArray since we're now passing the whole job object
    self.jobDetailsModalVisible = ko.observable(false);
    self.jobDetailsData = ko.observable();
    self.showJobDetails = function (job) {
        modalHandlers.showJobDetails(self, job);
    };

    self.actionModalVisible = ko.observable(false);
    self.actionModalHeader = ko.observable();
    self.actionDetails = {
        action: ko.observable(),
        button: ko.observable(),
        key: ko.observable(),
        repGroup: ko.observable(),
        state: ko.observable(),
        exited: ko.observable(),
        exitCode: ko.observable(),
        failReason: ko.observable(),
        count: ko.observable()
    };

    //-------------------------------------------------------------------------
    // ACTION HANDLING
    //-------------------------------------------------------------------------
    self.jobToActionDetails = function (job, action, button) {
        jobToActionDetails(self, job, action, button);
    };

    self.commitAction = function (all) {
        commitAction(self, all);
    };

    //-------------------------------------------------------------------------
    // ACTION CONFIRMATION HANDLERS
    //-------------------------------------------------------------------------
    self.confirmRetry = function (job) {
        actionHandlers.confirmRetry(self, job);
    };

    self.confirmRemoveFail = function (job) {
        actionHandlers.confirmRemoveFail(self, job);
    };

    self.confirmRemoveDep = function (job) {
        actionHandlers.confirmRemoveDep(self, job);
    };

    self.confirmRemovePend = function (job) {
        actionHandlers.confirmRemovePend(self, job);
    };

    self.confirmRemoveDelay = function (job) {
        actionHandlers.confirmRemoveDelay(self, job);
    };

    self.confirmKill = function (job) {
        actionHandlers.confirmKill(self, job);
    };

    self.confirmDead = function (job) {
        actionHandlers.confirmDead(self, job);
    };

    //-------------------------------------------------------------------------
    // SERVER MANAGEMENT
    //-------------------------------------------------------------------------
    self.confirmDeadServer = function (server) {
        actionHandlers.confirmDeadServer(self, server);
    };

    //-------------------------------------------------------------------------
    // MESSAGE HANDLING
    //-------------------------------------------------------------------------
    self.dismissMessage = function (si) {
        actionHandlers.dismissMessage(self, si);
    };

    self.dismissMessages = function (si) {
        actionHandlers.dismissMessages(self);
    };

    //-------------------------------------------------------------------------
    // JOB LOADING
    //-------------------------------------------------------------------------
    self.loadMoreJobs = function (job, event) {
        loadMoreJobs(self, job, event);
    };
}