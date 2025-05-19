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

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: `Memory Usage Distribution (${memoryValues.length} data points)`,
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
                min: minValue, // Set min value with padding
                max: maxValue, // Set max value with padding
                title: {
                    display: true,
                    text: 'Memory (MB)'
                },
                ticks: {
                    callback: function (value) {
                        // Ensure we don't get NaN values
                        return value ? value.mbIEC() : '0 B';
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
        title: `Memory Usage Distribution (${memoryValues.length} data points)`,
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
            itemDisplay: true,
            // Disable violin to show just boxplot with points
            violin: false
        }]
    };

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: `Disk Usage Distribution (${diskValues.length} data points)`,
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
                min: minValue, // Set min value with padding
                max: maxValue, // Set max value with padding
                title: {
                    display: true,
                    text: 'Disk (MB)'
                },
                ticks: {
                    callback: function (value) {
                        return value ? value.mbIEC() : '0 B';
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
        title: `Disk Usage Distribution (${diskValues.length} data points)`,
        data: chartData,
        options: chartOptions
    };
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
