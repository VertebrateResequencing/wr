import { initNumberPrototypes, initStringPrototypes, initKnockoutBindings } from '/js/utility.js';
import { StatusViewModel } from '/js/status-viewmodel.js';
import { setupCommandPathBehavior } from '/js/ui-helpers.js';

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

    // Set up command path truncation/expansion behavior
    setupCommandPathBehavior(svm);
});
