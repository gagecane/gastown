(function() {
    'use strict';

    // Cross-module helpers from core.js
    var escapeHtml = window.GT.escapeHtml;
    var showToast = window.GT.showToast;
    var ansiToHtml = window.GT.ansiToHtml;

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

    // ============================================
    // READY WORK PANEL
    // ============================================
    function loadReady() {
        var loading = document.getElementById('ready-loading');
        var table = document.getElementById('ready-table');
        var tbody = document.getElementById('ready-tbody');
        var empty = document.getElementById('ready-empty');
        var count = document.getElementById('ready-count');

        if (!loading || !table || !tbody) return;

        fetch('/api/ready')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.items && data.items.length > 0) {
                    table.style.display = 'table';
                    empty.style.display = 'none';
                    tbody.innerHTML = '';

                    data.items.forEach(function(item) {
                        var tr = document.createElement('tr');
                        var rowClass = '';
                        if (item.priority === 1) rowClass = 'ready-p1';
                        else if (item.priority === 2) rowClass = 'ready-p2';
                        tr.className = rowClass;

                        var priBadge = '';
                        if (item.priority === 1) priBadge = '<span class="badge badge-red">P1</span>';
                        else if (item.priority === 2) priBadge = '<span class="badge badge-orange">P2</span>';
                        else if (item.priority === 3) priBadge = '<span class="badge badge-yellow">P3</span>';
                        else priBadge = '<span class="badge badge-muted">P4</span>';

                        var sourceClass = item.source === 'town' ? 'ready-source ready-source-town' : 'ready-source';

                        tr.innerHTML =
                            '<td>' + priBadge + '</td>' +
                            '<td><span class="ready-id">' + escapeHtml(item.id) + '</span></td>' +
                            '<td><span class="ready-title">' + escapeHtml(item.title || '') + '</span></td>' +
                            '<td><span class="' + sourceClass + '">' + escapeHtml(item.source) + '</span></td>' +
                            '<td><button class="sling-btn" data-bead-id="' + escapeHtml(item.id) + '" title="Sling to rig">Sling</button></td>';
                        tbody.appendChild(tr);
                    });

                    if (count) count.textContent = data.summary.total;
                } else {
                    table.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load ready work';
                console.error('Ready work load error:', err);
            });
    }

    // Load ready work on page load
    loadReady();
    // Expose for refresh after HTMX swaps
    window.refreshReadyPanel = loadReady;


})();
