// dashboard/workTabs.js
// Work panel tabs (ready / in-flight / blocked).
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // WORK PANEL TABS
    // ============================================
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
