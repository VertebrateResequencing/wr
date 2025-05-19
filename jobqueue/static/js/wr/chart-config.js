/**
 * Chart Configuration Helper
 * Handles creation of chart configurations for various resource types
 */

/**
 * Creates a memory chart configuration
 * @param {Array} memoryValues - Array of memory values
 * @returns {Object} Chart configuration object
 */
export function createMemoryChartConfig(memoryValues) {
    // Format for boxplot - data for boxplot is an array of values
    const chartData = {
        labels: ['Memory Usage'],
        datasets: [{
            label: 'Memory Usage (MB)',
            backgroundColor: 'rgba(54, 162, 235, 0.5)',
            borderColor: 'rgb(54, 162, 235)',
            borderWidth: 1,
            data: [memoryValues],
            outlierBackgroundColor: 'rgba(54, 162, 235, 0.3)',
            outlierBorderColor: 'rgb(54, 162, 235)',
            outlierRadius: 3,
            // Show all individual data points
            itemRadius: 3,
            itemStyle: 'circle',
            itemBackgroundColor: 'rgba(54, 162, 235, 0.6)',
            itemBorderColor: 'rgb(54, 162, 235)',
            // Force display of points
            itemDisplay: true,
        }]
    };

    // Find min and max values for better y-axis scaling
    const minValue = Math.max(0, Math.min(...memoryValues) * 0.9); // Add 10% padding below
    const maxValue = Math.max(...memoryValues) * 1.1; // Add 10% padding above

    // Calculate the data range to determine tick formatting
    const dataRange = maxValue - minValue;
    const useDecimals = dataRange < 10; // Use decimals if range is small

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: `Memory Usage Distribution`,
                font: { size: 16 }
            },
            tooltip: {
                enabled: false // Disable tooltips completely
            },
            legend: {
                display: false // Hide legend since we only have one dataset
            }
        },
        scales: {
            y: {
                min: minValue,
                max: maxValue,
                title: {
                    display: true,
                    text: 'Memory (MB)'
                },
                ticks: {
                    // Configure step size and formatting based on data range
                    stepSize: useDecimals ? dataRange / 5 : undefined,
                    callback: function (value) {
                        if (!value) return '0 B';

                        // Use more precision for small ranges
                        if (useDecimals) {
                            // Format with 1-2 decimals for small ranges
                            return Number(value).toFixed(2).mbIEC();
                        }
                        return value.mbIEC();
                    }
                }
            },
            x: {
                title: {
                    display: false
                }
            }
        }
    };

    return {
        type: 'boxplot',
        title: `Memory Usage Distribution`,
        data: chartData,
        options: chartOptions
    };
}

/**
 * Creates a disk usage chart configuration
 * @param {Array} diskValues - Array of disk usage values
 * @returns {Object} Chart configuration object
 */
export function createDiskChartConfig(diskValues) {
    // Find min and max values for better y-axis scaling
    const minValue = Math.max(0, Math.min(...diskValues) * 0.9); // Add 10% padding below
    const maxValue = Math.max(...diskValues) * 1.1; // Add 10% padding above

    // Calculate the data range to determine tick formatting
    const dataRange = maxValue - minValue;
    const useDecimals = dataRange < 10; // Use decimals if range is small

    // Create data structure for boxplot
    const chartData = {
        labels: ['Disk Usage'],
        datasets: [{
            label: 'Disk Usage (MB)',
            backgroundColor: 'rgba(75, 192, 192, 0.5)',
            borderColor: 'rgb(75, 192, 192)',
            borderWidth: 1,
            data: [diskValues],
            outlierBackgroundColor: 'rgba(75, 192, 192, 0.3)',
            outlierBorderColor: 'rgb(75, 192, 192)',
            outlierRadius: 3,
            // Show all individual data points
            itemRadius: 3,
            itemStyle: 'circle',
            itemBackgroundColor: 'rgba(75, 192, 192, 0.6)',
            itemBorderColor: 'rgb(75, 192, 192)',
            // Force display of points
            itemDisplay: true
        }]
    };

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: `Disk Usage Distribution`,
                font: { size: 16 }
            },
            tooltip: {
                enabled: false // Disable tooltips completely
            },
            legend: {
                display: false // Hide legend since we only have one dataset
            }
        },
        scales: {
            y: {
                min: minValue,
                max: maxValue,
                title: {
                    display: true,
                    text: 'Disk (MB)'
                },
                ticks: {
                    // Configure step size and formatting based on data range
                    stepSize: useDecimals ? dataRange / 5 : undefined,
                    callback: function (value) {
                        if (!value) return '0 B';

                        // Use more precision for small ranges
                        if (useDecimals) {
                            // Format with 1-2 decimals for small ranges
                            return Number(value).toFixed(2).mbIEC();
                        }
                        return value.mbIEC();
                    }
                }
            },
            x: {
                title: {
                    display: false
                }
            }
        }
    };

    return {
        type: 'boxplot',
        title: `Disk Usage Distribution`,
        data: chartData,
        options: chartOptions
    };
}

/**
 * Creates a combined time chart configuration showing Wall Time and CPU Time side by side
 * @param {Array} walltimeValues - Array of wall time values in seconds
 * @param {Array} cputimeValues - Array of CPU time values in seconds
 * @returns {Object} Chart configuration object
 */
export function createCombinedTimeChartConfig(walltimeValues, cputimeValues) {
    // Find min and max values for better y-axis scaling
    const allTimeValues = [...walltimeValues, ...cputimeValues];
    const minValue = Math.max(0, Math.min(...allTimeValues) * 0.9); // Add 10% padding below
    const maxValue = Math.max(...allTimeValues) * 1.1; // Add 10% padding above

    // Format for boxplot - data for boxplot is arrays of values
    const chartData = {
        labels: ['Wall Time', 'CPU Time'],
        datasets: [{
            label: 'Time (seconds)',
            backgroundColor: 'rgba(255, 159, 64, 0.5)',
            borderColor: 'rgb(255, 159, 64)',
            borderWidth: 1,
            data: [walltimeValues, cputimeValues],
            outlierBackgroundColor: 'rgba(255, 159, 64, 0.3)',
            outlierBorderColor: 'rgb(255, 159, 64)',
            outlierRadius: 3,
            // Show all individual data points
            itemRadius: 3,
            itemStyle: 'circle',
            itemBackgroundColor: 'rgba(255, 159, 64, 0.6)',
            itemBorderColor: 'rgb(255, 159, 64)',
            // Force display of points
            itemDisplay: true,
        }]
    };

    // Calculate the data range to determine tick formatting
    const dataRange = maxValue - minValue;
    const useDecimals = dataRange < 5; // Use decimals if range is very small for time values

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: `Time Metrics Comparison`,
                font: { size: 16 }
            },
            tooltip: {
                enabled: false // Disable tooltips
            },
            legend: {
                display: false // Hide legend since we only have one dataset
            }
        },
        scales: {
            y: {
                min: minValue,
                max: maxValue,
                title: {
                    display: true,
                    text: 'Time (seconds)'
                },
                ticks: {
                    // Configure step size and formatting based on data range
                    stepSize: useDecimals ? dataRange / 5 : undefined,
                    // Ensure we don't get repeated values on the axis
                    count: useDecimals ? 6 : undefined,
                    callback: function (value) {
                        if (!value) return '0s';

                        // For small ranges, format with decimal precision
                        if (useDecimals) {
                            // Return the raw seconds with decimals for very small ranges
                            if (dataRange < 1) {
                                return value.toFixed(2) + 's';
                            }
                            // For slightly larger ranges (1-5s), use 1 decimal
                            return value.toFixed(1) + 's';
                        }

                        // Use the standard duration formatter for larger ranges
                        return value.toDuration();
                    }
                }
            },
            x: {
                title: {
                    display: true,
                    text: 'Time Metrics'
                }
            }
        }
    };

    return {
        type: 'boxplot',
        title: `Time Metrics Comparison`,
        data: chartData,
        options: chartOptions
    };
}

/**
 * Creates an execution timeline chart configuration
 * @param {Array} jobsData - Array of job data objects
 * @returns {Object} Chart configuration object
 */
export function createExecutionChartConfig(jobsData) {
    // Filter and prepare timeline data
    const timelineData = jobsData
        .filter(job => job.Started && (job.State === 'complete' || job.State === 'buried' || job.Ended))
        .map(job => ({
            x: job.Started,
            y: job.HostID || job.Host || job.RepGroup,
            start: job.Started,
            end: job.Ended || job.Started + job.Walltime,
            duration: (job.Ended || job.Started + job.Walltime) - job.Started,
            label: `${job.Cmd.substring(0, 30)}...`,
            state: job.State,
            id: job.Key
        }));

    if (timelineData.length === 0) {
        return null;
    }

    // Sort and deduplicate the y-axis labels
    const yLabels = [...new Set(timelineData.map(item => item.y))].sort();

    // Create data structure
    const chartData = {
        labels: yLabels,
        datasets: [{
            type: 'scatter',
            label: 'Jobs',
            data: timelineData.map(item => ({
                x: item.start,
                y: yLabels.indexOf(item.y),
                job: item
            })),
            backgroundColor: timelineData.map(item =>
                item.state === 'complete' ? 'rgba(40, 167, 69, 0.7)' :
                    item.state === 'buried' ? 'rgba(220, 53, 69, 0.7)' : 'rgba(0, 123, 255, 0.7)'),
            borderColor: timelineData.map(item =>
                item.state === 'complete' ? 'rgb(40, 167, 69)' :
                    item.state === 'buried' ? 'rgb(220, 53, 69)' : 'rgb(0, 123, 255)'),
            pointStyle: 'rect',
            pointRadius: 8,
            pointHoverRadius: 10
        }]
    };

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: 'Job Execution Timeline',
                font: { size: 16 }
            },
            tooltip: {
                callbacks: {
                    label: function (context) {
                        const job = context.raw.job;
                        return [
                            `Command: ${job.label}`,
                            `Started: ${job.start.toDate()}`,
                            `Duration: ${job.duration.toDuration()}`,
                            `Status: ${job.state}`
                        ];
                    }
                }
            }
        },
        scales: {
            x: {
                type: 'linear',
                title: {
                    display: true,
                    text: 'Timeline'
                },
                ticks: {
                    callback: function (value) {
                        return value.toDate();
                    }
                }
            },
            y: {
                type: 'category',
                labels: yLabels,
                title: {
                    display: true,
                    text: 'Host/Server'
                }
            }
        }
    };

    // Calculate timeline stats for HTML display
    const startTimes = timelineData.map(j => j.start);
    const endTimes = timelineData.map(j => j.end);
    const durations = timelineData.map(j => j.duration);
    const minStart = Math.min(...startTimes);
    const maxEnd = Math.max(...endTimes);

    const statsHtml = `
    <div class="stat-item"><span class="stat-label">Jobs:</span> ${timelineData.length}</div>
    <div class="stat-item"><span class="stat-label">Start:</span> ${minStart.toDate()}</div>
    <div class="stat-item"><span class="stat-label">End:</span> ${maxEnd.toDate()}</div>
    <div class="stat-item"><span class="stat-label">Span:</span> ${(maxEnd - minStart).toDuration()}</div>
    <div class="stat-item"><span class="stat-label">Avg Duration:</span> ${(durations.reduce((a, b) => a + b, 0) / durations.length).toDuration()}</div>
  `;

    return {
        type: 'scatter',
        title: 'Job Execution Timeline',
        data: chartData,
        options: chartOptions,
        statsHtml: statsHtml
    };
}

/**
 * Helper function to create histogram bins
 * @param {Array} data - Array of values to bin
 * @param {number} binCount - Number of bins to create
 * @returns {Array} Array of bin objects
 */
export function createHistogramBins(data, binCount) {
    if (data.length === 0) return [];

    const min = Math.min(...data);
    const max = Math.max(...data);
    const range = max - min;
    const binWidth = range / binCount;

    // Create the bins
    const bins = [];
    for (let i = 0; i < binCount; i++) {
        const binMin = min + (i * binWidth);
        const binMax = min + ((i + 1) * binWidth);
        bins.push({
            min: binMin,
            max: binMax,
            count: 0
        });
    }

    // Fill the bins
    data.forEach(value => {
        // Special case for the maximum value
        if (value === max) {
            bins[bins.length - 1].count++;
        } else {
            const binIndex = Math.floor((value - min) / binWidth);
            if (binIndex >= 0 && binIndex < bins.length) {
                bins[binIndex].count++;
            }
        }
    });

    return bins;
}

/**
 * Creates a time-based chart configuration (walltime or cputime)
 * @param {Array} timeValues - Array of time values
 * @param {string} timeType - Type of time ('walltime' or 'cputime')
 * @returns {Object} Chart configuration object
 */
export function createTimeChartConfig(timeValues, timeType) {
    const isWallTime = timeType === 'walltime';
    const title = isWallTime ? 'Wall Time Distribution' : 'CPU Time Distribution';

    // Create bins for the histogram
    const binCount = Math.min(Math.ceil(Math.sqrt(timeValues.length)), 15);
    const bins = createHistogramBins(timeValues, binCount);

    // Create data structure
    const chartData = {
        labels: bins.map(bin => `${bin.min.toDuration()} - ${bin.max.toDuration()}`),
        datasets: [{
            label: isWallTime ? 'Wall Time (seconds)' : 'CPU Time (seconds)',
            backgroundColor: isWallTime ? 'rgba(255, 159, 64, 0.5)' : 'rgba(153, 102, 255, 0.5)',
            borderColor: isWallTime ? 'rgb(255, 159, 64)' : 'rgb(153, 102, 255)',
            borderWidth: 1,
            data: bins.map(bin => bin.count)
        }]
    };

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: title,
                font: { size: 16 }
            },
            tooltip: {
                callbacks: {
                    title: function (context) {
                        return context[0].label;
                    },
                    label: function (context) {
                        return `${context.raw} jobs`;
                    }
                }
            }
        },
        scales: {
            y: {
                beginAtZero: true,
                title: {
                    display: true,
                    text: 'Number of Jobs'
                }
            },
            x: {
                title: {
                    display: true,
                    text: isWallTime ? 'Wall Time' : 'CPU Time'
                }
            }
        }
    };

    return {
        type: 'bar',
        title: title,
        data: chartData,
        options: chartOptions
    };
}
