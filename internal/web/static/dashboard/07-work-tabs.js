// Dashboard module: work-panel-tabs
// Extracted from dashboard.js lines 1501-1539.
// Depends on: window.gtDash (escapeHtml, ansiToHtml, showToast, showOutput,
// updateConnectionStatus — aliased below).
(function() {
    'use strict';

    var gtDash = window.gtDash || {};
    var escapeHtml = gtDash.escapeHtml;
    var ansiToHtml = gtDash.ansiToHtml;
    var showToast = gtDash.showToast;
    var showOutput = gtDash.showOutput;
    var updateConnectionStatus = gtDash.updateConnectionStatus;

    function switchWorkTab(tab) {
        // Update active tab button
        document.querySelectorAll('.panel-tabs .tab-btn').forEach(function(btn) {
            btn.classList.remove('active');
            if (btn.getAttribute('data-tab') === tab) {
                btn.classList.add('active');
            }
        });

        // Filter rows based on tab
        var rows = document.querySelectorAll('#work-table tbody tr');
        rows.forEach(function(row) {
            var status = row.getAttribute('data-status') || 'ready';
            if (tab === 'all') {
                row.style.display = '';
            } else if (tab === 'ready' && status === 'ready') {
                row.style.display = '';
            } else if (tab === 'progress' && status === 'progress') {
                row.style.display = '';
            } else {
                row.style.display = 'none';
            }
        });

        // Update count
        var visibleCount = 0;
        rows.forEach(function(row) {
            if (row.style.display !== 'none') visibleCount++;
        });
        var countEl = document.querySelector('#work-panel .count');
        if (countEl) countEl.textContent = visibleCount;
    }
    window.switchWorkTab = switchWorkTab;

    // Initialize work panel to "Ready" tab on load
    setTimeout(function() {
        switchWorkTab('ready');
    }, 100);

})();
