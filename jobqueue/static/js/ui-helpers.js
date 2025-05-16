/* UI Helpers
 * UI-related helper functions for the WR status page.
 */

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

    // Run the check after a slight delay to ensure DOM is rendered
    setTimeout(checkForTruncation, 100);

    // Also check when window is resized
    window.addEventListener('resize', function () {
        setTimeout(checkForTruncation, 100);
    });

    // Check after job details are loaded
    viewModel.sortableRepGroups.subscribe(function () {
        setTimeout(checkForTruncation, 100);
    });

    // Setup MutationObserver to detect DOM changes and check truncation
    const observer = new MutationObserver(function (mutations) {
        for (const mutation of mutations) {
            if (mutation.addedNodes.length > 0) {
                setTimeout(checkForTruncation, 100);
                break;
            }
        }
    });

    // Start observing the document body for child list changes
    observer.observe(document.getElementById('status'), {
        childList: true,
        subtree: true
    });

    // Check when new jobs are loaded
    if (viewModel.detailsOA) {
        viewModel.detailsOA.subscribe(function (jobs) {
            // Only run if there are jobs and this is not an initial load (length > 1)
            if (jobs.length > 1) {
                setTimeout(checkForTruncation, 100);
            }
        });
    }
}

/**
 * Updates the view after loading more jobs
 * @param {StatusViewModel} viewModel - The main view model
 * @param {number} previousLength - The previous length of the jobs array
 */
export function handleMoreJobsLoaded(viewModel, previousLength) {
    // Ensure truncation is checked for new elements
    setTimeout(function () {
        checkForTruncation();

        // Find new elements
        const newElements = document.querySelectorAll('.panel');
        if (newElements.length > previousLength) {
            // Focus stays on the page where it was
            // No need to scroll as the new jobs are appended below
        }
    }, 100);
}
