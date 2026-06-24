/* Modal Handlers
 * Handles modals for the WR status page.
 */
import {
    createModifyForm,
    createModifyRequest,
    jobCanModify,
    replaceModifiedJobs,
    trimTrailingNewline
} from '/js/wr/modify-job.js';
import { capitalizeFirstLetter, setupLiveWalltime } from '/js/wr/utility.js';

const modifyFormObservableFields = [
    'key',
    'cmd',
    'cwd',
    'cwdMatters',
    'changeHome',
    'reqGrp',
    'memory',
    'time',
    'cpus',
    'disk',
    'priority',
    'retries',
    'override',
    'noRetryOverWalltime',
    'limitGrps',
    'modules',
    'deps',
    'cmdDeps',
    'onFailure',
    'onSuccess',
    'onExit',
    'other',
    'mounts',
    'monitorDocker',
    'withDocker',
    'withSingularity',
    'containerMounts',
    'env',
];

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
        const request = {
            Request: viewModel.actionDetails.action(),
            Key: viewModel.actionDetails.key(),
        };

        if (viewModel.actionDetails.action() === 'rerun') {
            request.RepGroup = viewModel.actionDetails.repGroup();
        }

        viewModel.ws.send(JSON.stringify(request));
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
 * Shows the single-job Modify modal.
 * @param {StatusViewModel} viewModel - The main view model
 * @param {object} job - The job object
 */
export function showModifyJob(viewModel, job) {
    if (!jobCanModify(job)) {
        return;
    }

    viewModel.modifyJobError('');
    viewModel.modifyJobForm(observableModifyForm(createModifyForm(job)));
    viewModel.modifyJobModalVisible(true);
}

/**
 * Submits the single-job Modify modal.
 * @param {StatusViewModel} viewModel - The main view model
 * @returns {Promise<boolean>} Whether the request succeeded
 */
export function submitModifyJob(viewModel) {
    const form = viewModel.modifyJobForm();
    if (!form) {
        return Promise.resolve(false);
    }

    let request;

    try {
        request = createModifyRequest(form, viewModel.token);
    } catch (error) {
        viewModel.modifyJobError(trimTrailingNewline(error.message || String(error)));

        return Promise.resolve(false);
    }

    viewModel.modifyJobError('');
    viewModel.modifyJobSubmitting(true);

    return fetch(request.url, request.options)
        .then(async response => {
            if (!response.ok) {
                viewModel.modifyJobError(trimTrailingNewline(await response.text()));

                return false;
            }

            const body = await response.json();
            replaceVisibleModifiedJobs(viewModel, body);
            viewModel.modifyJobModalVisible(false);

            return true;
        })
        .catch(error => {
            viewModel.modifyJobError(trimTrailingNewline(error.message || String(error)));

            return false;
        })
        .finally(() => {
            viewModel.modifyJobSubmitting(false);
        });
}

function observableModifyForm(form) {
    const observableForm = {
        originalJob: form.originalJob,
        originalCmdDeps: form.originalCmdDeps,
        originalOnFailure: form.originalOnFailure,
        originalOnSuccess: form.originalOnSuccess,
        originalOnExit: form.originalOnExit,
        originalOther: form.originalOther,
        originalMounts: form.originalMounts,
        originalEnv: form.originalEnv,
    };

    for (const field of modifyFormObservableFields) {
        observableForm[field] = ko.observable(form[field]);
    }

    return observableForm;
}

function replaceVisibleModifiedJobs(viewModel, response) {
    if (!viewModel.detailsOA) {
        return;
    }

    const updated = replaceModifiedJobs(viewModel.detailsOA(), response);

    for (const job of updated) {
        setupLiveWalltime(job, job.Walltime, viewModel);
    }

    viewModel.detailsOA(updated);
}

/**
 * Setup functions for each modal type
 */
export const modalHandlers = {
    showJobDetails: function (viewModel, job) {
        viewModel.jobDetailsData(job);
        viewModel.jobDetailsModalVisible(true);
    },

    showModifyJob,
    submitModifyJob
};
