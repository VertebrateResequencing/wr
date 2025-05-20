/* UI Helpers
 * UI-related helper functions for the WR status page.
 */

/**
 * Creates a debounced function that delays invoking func until after wait milliseconds
 * @param {Function} func - The function to debounce
 * @param {number} wait - The number of milliseconds to delay
 * @returns {Function} The debounced function
 */
function debounce(func, wait) {
    let timeout;
    return function () {
        const context = this;
        const args = arguments;
        clearTimeout(timeout);
        timeout = setTimeout(() => {
            func.apply(context, args);
        }, wait);
    };
}

/**
 * Sets up command path truncation and expansion functionality
 * @param {StatusViewModel} viewModel - The main view model
 */
export function setupCommandPathBehavior(viewModel) {
    // Add the one-time expansion function to the viewModel
    viewModel.togglePathExpansion = function (data, event) {
        const element = event.currentTarget;

        // Only expand if the element is truncated (has the class) and not already expanded
        if (element.classList.contains('truncated') && !element.classList.contains('expanded')) {
            // Add expanded class (one-time, doesn't toggle off)
            element.classList.add('expanded');
        }
    };

    // Check for truncation after rendering and window resize
    function checkForTruncation() {
        document.querySelectorAll('.command-path').forEach(function (el) {
            // If already expanded, leave it expanded
            if (el.classList.contains('expanded')) {
                return;
            }

            // If the scrollWidth is greater than the clientWidth, the text is truncated
            if (el.scrollWidth > el.clientWidth) {
                el.classList.add('truncated');
            } else {
                el.classList.remove('truncated');
            }
        });
    }

    // Create a debounced version of the function
    const debouncedCheckForTruncation = debounce(checkForTruncation, 100);

    // Run the check after a slight delay to ensure DOM is rendered
    debouncedCheckForTruncation();

    // Use debounced function for resize events
    window.addEventListener('resize', debouncedCheckForTruncation);

    // Check after job details are loaded
    viewModel.sortableRepGroups.subscribe(function () {
        debouncedCheckForTruncation();
    });

    // Setup MutationObserver to detect DOM changes and check truncation
    const observer = new MutationObserver(function (mutations) {
        for (const mutation of mutations) {
            if (mutation.addedNodes.length > 0) {
                debouncedCheckForTruncation();
                break;
            }
        }
    });

    // Start observing the document body for child list changes
    const statusElement = document.getElementById('status');
    if (statusElement) {
        observer.observe(statusElement, {
            childList: true,
            subtree: true
        });
    }

    // Check when new jobs are loaded
    if (viewModel.detailsOA) {
        viewModel.detailsOA.subscribe(function (jobs) {
            // Only run if there are jobs and this is not an initial load (length > 1)
            if (jobs.length > 1) {
                debouncedCheckForTruncation();
            }
        });
    }
}
