// Dashboard module: ready-work-panel
// Extracted from dashboard.js lines 1543-1603.
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
