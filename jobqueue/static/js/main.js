import { initNumberPrototypes, initStringPrototypes, initKnockoutBindings } from '/js/utility.js';
import { StatusViewModel } from '/js/status-viewmodel.js';

// Initialize prototype extensions
initNumberPrototypes();
initStringPrototypes();
initKnockoutBindings();

// Initialize the application
document.addEventListener('DOMContentLoaded', () => {
    ko.options.deferUpdates = true;
    const svm = new StatusViewModel();
    ko.applyBindings(svm, document.getElementById('status'));
});
