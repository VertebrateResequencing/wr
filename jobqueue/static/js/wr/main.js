import { initKnockoutBindings, mbIEC, toDuration, toDate } from '/js/wr/utility.js';
import { StatusViewModel } from '/js/wr/status-viewmodel.js';
import { setupCommandPathBehavior } from '/js/wr/ui-helpers.js';

// Make utility functions globally available for templates
if (typeof window.wrUtils === 'undefined') {
    window.wrUtils = {};
}
window.wrUtils.mbIEC = mbIEC;
window.wrUtils.toDuration = toDuration;
window.wrUtils.toDate = toDate;

// Initialize Knockout bindings
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
