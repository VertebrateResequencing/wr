import { initNumberPrototypes, initStringPrototypes, initKnockoutBindings } from '/js/wr/utility.js';
import { StatusViewModel } from '/js/wr/status-viewmodel.js';
import { setupCommandPathBehavior } from '/js/wr/ui-helpers.js';

// Initialize prototype extensions
initNumberPrototypes();
initStringPrototypes();
initKnockoutBindings();

// Initialize the application when DOM is fully loaded
document.addEventListener('DOMContentLoaded', () => {
    // Enable deferred updates for better performance with Knockout
    ko.options.deferUpdates = true;

    // Create and bind the main view model
    const svm = new StatusViewModel();
    ko.applyBindings(svm, document.getElementById('status'));

    // Mark as initialized and show content
    document.body.classList.add('ko-initialized');

    // Set up command path truncation/expansion behavior
    setupCommandPathBehavior(svm);
});
