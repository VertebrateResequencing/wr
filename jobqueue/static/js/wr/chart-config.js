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
    const rawScatterData = jobsData
        .filter(job => job.Started && (job.State === 'complete' || job.State === 'buried' || job.Ended))
        .map(job => {
            const startTime = job.Started;
            const endTime = job.Ended || (job.Started + job.Walltime);
            const duration = endTime - startTime;

            return {
                x: startTime, // Start time for x-axis
                y: duration,  // Duration for y-axis (elapsed time)
                cmd: job.Cmd.substring(0, 30) + "...",
                state: job.State,
                hostid: job.HostID || job.Host || job.RepGroup,
                startDate: startTime,
                endDate: endTime,
                duration: duration
            };
        });

    if (rawScatterData.length === 0) {
        return null;
    }

    // Group overlapping points
    const pointGroups = {};
    const groupedData = [];

    // Group points based on x,y coordinates (round to the nearest second to account for tiny differences)
    rawScatterData.forEach(point => {
        // Create a key that represents this position (rounded to 1 decimal place for grouping)
        const key = `${Math.round(point.x)},${Math.round(point.y)}`;

        if (!pointGroups[key]) {
            pointGroups[key] = {
                points: [point],
                x: point.x,
                y: point.y,
                count: 1,
                state: point.state,
                // Store the first command for the tooltip
                cmd: point.cmd,
                startDate: point.startDate,
                endDate: point.endDate,
                duration: point.duration,
                hostid: point.hostid
            };
            groupedData.push(pointGroups[key]);
        } else {
            pointGroups[key].points.push(point);
            pointGroups[key].count++;

            // If we have commands with different states, prioritize showing buried ones
            if (point.state === 'buried') {
                pointGroups[key].state = 'buried';
            }
        }
    });

    // Create scatter data from the grouped points
    const scatterData = groupedData.map(group => ({
        x: group.x,
        y: group.y,
        count: group.count,
        cmd: group.count > 1 ? `${group.count} jobs at this point` : group.cmd,
        state: group.state,
        hostid: group.hostid,
        startDate: group.startDate,
        endDate: group.endDate,
        duration: group.duration,
        // Store all points for detailed tooltip
        allPoints: group.points
    }));

    // Find min and max values for y-axis (durations) with padding
    const durations = scatterData.map(item => item.y);
    const minDurationPadded = Math.max(0, Math.min(...durations) * 0.9); // Add 10% padding below
    const maxDurationPadded = Math.max(...durations) * 1.1; // Add 10% padding above

    // Calculate x-axis (time) bounds more carefully to prevent absurd ranges
    const startTimes = scatterData.map(item => item.x);
    const minTime = Math.min(...startTimes);
    const maxTime = Math.max(...startTimes);
    const timeRange = maxTime - minTime;

    // Add padding to the time axis - if range is very small, use at least 60 seconds of padding
    // This ensures that even jobs occurring within the same minute have reasonable x-axis bounds
    const timeAxisPadding = Math.max(timeRange * 0.05, 60);
    const minStartTime = minTime - timeAxisPadding;
    const maxStartTime = maxTime + timeAxisPadding;

    // Calculate the data range to determine tick formatting
    const dataRange = maxDurationPadded - minDurationPadded;
    const useDecimals = dataRange < 5; // Use decimals if range is small (< 5 seconds)

    // Create data structure
    const chartData = {
        datasets: [{
            type: 'scatter',
            label: 'Jobs',
            data: scatterData,
            backgroundColor: scatterData.map(item =>
                item.state === 'complete' ? 'rgba(40, 167, 69, 0.7)' :
                    item.state === 'buried' ? 'rgba(220, 53, 69, 0.7)' : 'rgba(0, 123, 255, 0.7)'),
            borderColor: scatterData.map(item =>
                item.state === 'complete' ? 'rgb(40, 167, 69)' :
                    item.state === 'buried' ? 'rgb(220, 53, 69)' : 'rgb(0, 123, 255)'),
            borderWidth: 1,
            pointStyle: 'circle',
            // Scale the point radius based on the count (min size 4, max size 15)
            pointRadius: scatterData.map(item => {
                const baseSize = 4;
                const maxSize = 15;
                const scaleFactor = 1.5;
                // Use logarithmic scaling for better visualization when there's a big difference in counts
                return Math.min(baseSize + Math.log(item.count) * scaleFactor, maxSize);
            }),
            hoverRadius: scatterData.map(item => {
                const baseSize = 6;
                const maxSize = 18;
                const scaleFactor = 2;
                return Math.min(baseSize + Math.log(item.count) * scaleFactor, maxSize);
            }),
            // Add hover effects to make clusters more visible
            hoverBorderWidth: scatterData.map(item => item.count > 1 ? 2 : 1),
            hoverBorderColor: scatterData.map(item =>
                item.count > 1 ? 'rgba(255, 255, 255, 0.8)' :
                    (item.state === 'complete' ? 'rgb(40, 167, 69)' :
                        item.state === 'buried' ? 'rgb(220, 53, 69)' : 'rgb(0, 123, 255)')
            )
        }]
    };

    // Create chart options
    const chartOptions = {
        responsive: true,
        plugins: {
            title: {
                display: true,
                text: 'Job Duration vs. Start Time',
                font: { size: 16 }
            },
            tooltip: {
                callbacks: {
                    label: function (context) {
                        const item = context.raw;

                        // For single points, show simple information
                        if (item.count === 1) {
                            return [
                                `Command: ${item.cmd}`,
                                `Start: ${item.startDate.toDate()}`,
                                `End: ${item.endDate.toDate()}`,
                                `Duration: ${item.duration.toDuration()}`,
                                `Host: ${item.hostid}`,
                                `Status: ${item.state}`
                            ];
                        }

                        // For grouped points, show summary with commands from the first few jobs
                        const maxToShow = Math.min(3, item.allPoints.length);
                        const result = [
                            `${item.count} jobs at this point`,
                            `Start time: ${item.startDate.toDate()}`,
                            `Duration: ${item.duration.toDuration()}`,
                            `Status: ${item.count > 1 ? 'Mixed' : item.state}`
                        ];

                        // Add commands for up to 3 jobs directly in the tooltip
                        for (let i = 0; i < maxToShow; i++) {
                            result.push(`Job ${i + 1}: ${item.allPoints[i].cmd}`);
                        }

                        // Add ellipsis if there are more jobs
                        if (item.count > maxToShow) {
                            result.push(`...and ${item.count - maxToShow} more`);
                        }

                        return result;
                    }
                }
            },
            legend: {
                display: false
            }
        },
        scales: {
            x: {
                type: 'linear',
                min: minStartTime,
                max: maxStartTime,
                title: {
                    display: true,
                    text: 'Start Time'
                },
                ticks: {
                    callback: function (value) {
                        return value.toDate();
                    },
                    // Rotate the labels to prevent overlapping
                    maxRotation: 45,
                    minRotation: 45,
                    autoSkip: true,
                    autoSkipPadding: 15,
                    padding: 10
                }
            },
            y: {
                min: minDurationPadded,
                max: maxDurationPadded,
                title: {
                    display: true,
                    text: 'Duration (seconds)'
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
            }
        }
    };

    // Calculate stats for HTML display - use raw min/max for stats (no padding)
    const minStart = Math.min(...startTimes);
    const maxStart = Math.max(...startTimes);
    const avgDuration = durations.reduce((a, b) => a + b, 0) / durations.length;
    const minDuration = Math.min(...durations);
    const maxDuration = Math.max(...durations);

    // Update the stats HTML to include information about grouping
    const statsHtml = `
    <div class="stat-item"><span class="stat-label">Jobs:</span> ${rawScatterData.length}</div>
    <div class="stat-item"><span class="stat-label">Unique Points:</span> ${scatterData.length}</div>
    <div class="stat-item"><span class="stat-label">Grouped Points:</span> ${scatterData.filter(p => p.count > 1).length}</div>
    <div class="stat-item"><span class="stat-label">First Job:</span> ${minStart.toDate()}</div>
    <div class="stat-item"><span class="stat-label">Last Job:</span> ${maxStart.toDate()}</div>
    <div class="stat-item"><span class="stat-label">Min Duration:</span> ${minDuration.toDuration()}</div>
    <div class="stat-item"><span class="stat-label">Max Duration:</span> ${maxDuration.toDuration()}</div>
    <div class="stat-item"><span class="stat-label">Avg Duration:</span> ${avgDuration.toDuration()}</div>
    `;

    return {
        type: 'scatter',
        title: 'Job Duration vs. Start Time',
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

    // Calculate min and max for y-axis scaling
    const counts = bins.map(bin => bin.count);
    const maxCount = Math.max(...counts) * 1.1; // Add 10% padding above
    const minCount = 0;

    // Calculate the data range to determine tick formatting
    const dataRange = maxCount - minCount;
    const useDecimals = dataRange < 5; // Use decimals if range is very small

    // For the x-axis (timeValues), calculate appropriate min and max with padding
    const minTime = Math.max(0, Math.min(...timeValues) * 0.9); // Add 10% padding below
    const maxTime = Math.max(...timeValues) * 1.1; // Add 10% padding above

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
                min: minCount,
                max: maxCount,
                beginAtZero: true,
                title: {
                    display: true,
                    text: 'Number of Jobs'
                },
                ticks: {
                    // Configure step size and formatting based on data range
                    stepSize: useDecimals ? dataRange / 5 : undefined,
                    // Ensure we don't get repeated values on the axis
                    count: useDecimals ? 6 : undefined,
                    callback: function (value) {
                        // For small ranges with decimal values
                        if (useDecimals && !Number.isInteger(value)) {
                            return value.toFixed(1);
                        }
                        return value;
                    }
                }
            },
            x: {
                // Apply min/max with padding to x-axis for histogram
                min: minTime,
                max: maxTime,
                title: {
                    display: true,
                    text: isWallTime ? 'Wall Time' : 'CPU Time'
                },
                ticks: {
                    callback: function (value) {
                        return value.toDuration();
                    }
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
