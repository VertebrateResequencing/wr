/* Modify Job Helpers
 * Pure browser-side helpers for editing a single WR job.
 */

const editableJobStates = new Set(['delayed', 'ready', 'dependent', 'buried']);
const commandDependencyPattern = /^(.*) \[([^\]]*)\]$/;

/**
 * Returns true when a status row can be edited through the web UI.
 * @param {object} job - Job status row.
 * @returns {boolean} Whether the row is editable.
 */
export function jobCanModify(job) {
    return Boolean(job && editableJobStates.has(valueOf(job.State)));
}

/**
 * Builds the plain form model used by the Modify modal.
 * @param {object} job - Job status row.
 * @returns {object} Plain form fields.
 */
export function createModifyForm(job) {
    const dependencies = splitDependencies(job.Dependencies || []);
    const behaviours = parseBehaviourMapping(job.Behaviours);
    const onFailure = jsonArrayText(behaviours.on_failure);
    const onSuccess = jsonArrayText(behaviours.on_success);
    const onExit = jsonArrayText(behaviours.on_exit);
    const cmdDeps = jsonArrayText(dependencies.cmdDeps);
    const other = linesFromList(job.OtherRequests || []);
    const mounts = job.Mounts || '';
    const env = linesFromList(job.EnvOverrides || []);

    return {
        key: job.Key || '',
        cmd: job.Cmd || '',
        cwd: job.CwdBase || '',
        cwdMatters: Boolean(job.CwdMatters),
        changeHome: Boolean(job.HomeChanged),
        reqGrp: job.ReqGroup || '',
        memory: memoryText(job.ExpectedRAM),
        time: durationText(job.ExpectedTime),
        cpus: numberText(job.Cores),
        disk: numberText(job.RequestedDisk),
        priority: numberText(job.Priority),
        retries: numberText(job.Retries),
        override: numberText(job.Override),
        noRetryOverWalltime: durationText(job.NoRetryOverWalltime),
        limitGrps: linesFromList(job.LimitGroups || []),
        modules: linesFromList(job.Modules || []),
        deps: linesFromList(dependencies.deps),
        cmdDeps,
        onFailure,
        onSuccess,
        onExit,
        other,
        mounts,
        monitorDocker: job.MonitorDocker || '',
        withDocker: job.WithDocker || '',
        withSingularity: job.WithSingularity || '',
        containerMounts: job.ContainerMounts || '',
        env,
        originalJob: job,
        originalCmdDeps: cmdDeps,
        originalOnFailure: onFailure,
        originalOnSuccess: onSuccess,
        originalOnExit: onExit,
        originalOther: other,
        originalMounts: mounts,
        originalEnv: env,
    };
}

/**
 * Builds the PATCH payload for a Modify form.
 * @param {object} form - Plain or Knockout-observable form fields.
 * @returns {object} REST PATCH payload.
 */
export function createModifyPayload(form) {
    const payload = {
        cmd: textField(form, 'cmd'),
        cwd: textField(form, 'cwd'),
        cwd_matters: boolField(form, 'cwdMatters'),
        change_home: boolField(form, 'changeHome'),
        req_grp: textField(form, 'reqGrp'),
        memory: textField(form, 'memory'),
        time: textField(form, 'time'),
        cpus: numberField(form, 'cpus'),
        disk: intField(form, 'disk'),
        priority: intField(form, 'priority'),
        retries: intField(form, 'retries'),
        override: intField(form, 'override'),
        no_retry_over_walltime: textField(form, 'noRetryOverWalltime') || '0s',
        limit_grps: linesFromText(textField(form, 'limitGrps')),
        modules: linesFromText(textField(form, 'modules')),
        deps: linesFromText(textField(form, 'deps')),
        monitor_docker: textField(form, 'monitorDocker'),
        with_docker: textField(form, 'withDocker'),
        with_singularity: textField(form, 'withSingularity'),
        container_mounts: textField(form, 'containerMounts'),
    };

    setJSONPayloadField(payload, 'cmd_deps', form, 'cmdDeps', 'originalCmdDeps');
    setJSONPayloadField(payload, 'on_failure', form, 'onFailure', 'originalOnFailure');
    setJSONPayloadField(payload, 'on_success', form, 'onSuccess', 'originalOnSuccess');
    setJSONPayloadField(payload, 'on_exit', form, 'onExit', 'originalOnExit');
    setOtherPayloadField(payload, form);
    setMountPayloadField(payload, form);
    setEnvPayloadField(payload, form);

    return payload;
}

/**
 * Builds a fetch request for the Modify PATCH call.
 * @param {object} form - Plain or Knockout-observable form fields.
 * @param {string} token - Bearer token from the status page URL.
 * @returns {object} Fetch URL and options.
 */
export function createModifyRequest(form, token) {
    const headers = {
        'Content-Type': 'application/json',
    };

    if (token) {
        headers.Authorization = `Bearer ${token}`;
    }

    return {
        url: `/rest/v1/jobs/${encodeURIComponent(textField(form, 'key'))}`,
        options: {
            method: 'PATCH',
            headers,
            body: JSON.stringify(createModifyPayload(form)),
        },
    };
}

/**
 * Applies a successful JobModifyResponse to the visible details rows.
 * @param {Array<object>} details - Current visible rows.
 * @param {object} response - REST modify response.
 * @returns {Array<object>} Updated visible rows.
 */
export function replaceModifiedJobs(details, response) {
    const rows = Array.isArray(details) ? details.slice() : [];
    const modified = response?.modified || response?.Modified || {};
    const jobs = response?.jobs || response?.Jobs || [];

    if (!Array.isArray(jobs) || jobs.length === 0) {
        return rows;
    }

    let updated = rows;

    for (const job of jobs) {
        const oldKey = modified[job.Key] || modified[job.key] || job.Key;
        const index = updated.findIndex(row => row.Key === oldKey || row.Key === job.Key);
        const previous = index >= 0 ? updated[index] : {};
        const replacement = normalizeReturnedJob(previous, job);

        if (index >= 0) {
            updated.splice(index, 1, replacement);
        } else {
            updated.push(replacement);
        }

        if (oldKey && oldKey !== replacement.Key) {
            updated = updated.filter(row => row.Key !== oldKey);
        }
    }

    return updated;
}

/**
 * Removes response-body newlines that http.Error appends.
 * @param {string} text - Error response body.
 * @returns {string} Text without trailing newlines.
 */
export function trimTrailingNewline(text) {
    return String(text || '').replace(/(\r?\n)+$/g, '');
}

function valueOf(value) {
    return typeof value === 'function' ? value() : value;
}

function textField(form, field) {
    return String(valueOf(form[field]) ?? '');
}

function boolField(form, field) {
    return Boolean(valueOf(form[field]));
}

function numberField(form, field) {
    const value = Number(textField(form, field));
    return Number.isNaN(value) ? 0 : value;
}

function intField(form, field) {
    return Math.trunc(numberField(form, field));
}

function memoryText(megabytes) {
    const value = Number(megabytes || 0);
    return `${Number.isInteger(value) ? value : value.toString()}M`;
}

function numberText(value) {
    if (value === undefined || value === null) {
        return '';
    }

    return String(value);
}

function durationText(seconds) {
    const value = Number(seconds || 0);
    if (!value) {
        return '';
    }

    if (value % 86400 === 0) {
        return `${value / 86400}d`;
    }

    if (value % 3600 === 0) {
        return `${value / 3600}h`;
    }

    if (value % 60 === 0) {
        return `${value / 60}m`;
    }

    return `${value}s`;
}

function linesFromList(list) {
    return Array.isArray(list) ? list.join('\n') : '';
}

function linesFromText(text) {
    return String(text || '')
        .split(/\r?\n/)
        .map(line => line.trim())
        .filter(line => line !== '');
}

function splitDependencies(dependencies) {
    const deps = [];
    const cmdDeps = [];

    for (const dependency of dependencies) {
        const match = String(dependency).match(commandDependencyPattern);
        if (match) {
            cmdDeps.push({ cmd: match[1], cwd: match[2] });
        } else {
            deps.push(dependency);
        }
    }

    return { deps, cmdDeps };
}

function parseBehaviourMapping(behaviours) {
    if (!behaviours) {
        return {};
    }

    return JSON.parse(behaviours);
}

function jsonArrayText(value) {
    return Array.isArray(value) && value.length > 0 ? JSON.stringify(value) : '';
}

function parseJSONList(text) {
    const value = String(text || '').trim();
    if (value === '') {
        return [];
    }

    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
}

function setJSONPayloadField(payload, payloadField, form, formField, originalField) {
    const current = textField(form, formField).trim();
    const original = textField(form, originalField).trim();

    if (current !== '' || original !== '') {
        payload[payloadField] = parseJSONList(current);
    }
}

function setOtherPayloadField(payload, form) {
    const current = textField(form, 'other').trim();
    const original = textField(form, 'originalOther').trim();

    if (current === '' && original === '') {
        return;
    }

    const other = {};
    for (const line of linesFromText(current)) {
        const separator = line.indexOf(':');
        if (separator < 0) {
            other[line] = '';
        } else {
            other[line.slice(0, separator).trim()] = line.slice(separator + 1).trim();
        }
    }

    payload.other = other;
}

function setMountPayloadField(payload, form) {
    const current = textField(form, 'mounts').trim();
    const original = textField(form, 'originalMounts').trim();

    if (current !== '' || original !== '') {
        payload.mounts = parseJSONList(current);
    }
}

function setEnvPayloadField(payload, form) {
    const current = textField(form, 'env');
    const original = textField(form, 'originalEnv');

    if (current !== original || original.trim() !== '') {
        payload.env = linesFromText(current);
    }
}

function normalizeReturnedJob(previous, returned) {
    const next = { ...returned };

    if ((!Array.isArray(next.Env) || next.Env.length === 0) && Array.isArray(previous.Env)) {
        next.Env = effectiveEnvWithOverrides(previous.Env, previous.EnvOverrides || [], next.EnvOverrides || []);
    }

    return next;
}

function effectiveEnvWithOverrides(previousEnv, previousOverrides, nextOverrides) {
    const previousOverrideNames = envNames(previousOverrides);
    const nextOverrideNames = envNames(nextOverrides);
    const overrideNames = new Set([...previousOverrideNames, ...nextOverrideNames]);
    const inherited = previousEnv.filter(entry => !overrideNames.has(envName(entry)));

    return inherited.concat(nextOverrides);
}

function envNames(entries) {
    return new Set((entries || []).map(envName));
}

function envName(entry) {
    return String(entry).split('=')[0];
}
