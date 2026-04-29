// Dashboard module: sling-buttons
// Extracted from dashboard.js lines 2666-2767.
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

    var activeSlingDropdown = null;

    function closeSlingDropdown() {
        if (activeSlingDropdown) {
            activeSlingDropdown.remove();
            activeSlingDropdown = null;
        }
    }

    function openSlingDropdown(btn) {
        closeSlingDropdown();

        var beadId = btn.getAttribute('data-bead-id');
        if (!beadId) return;

        var dropdown = document.createElement('div');
        dropdown.className = 'sling-dropdown';
        dropdown.innerHTML = '<div class="sling-dropdown-loading">Loading rigs...</div>';

        // Position dropdown below the button
        var rect = btn.getBoundingClientRect();
        dropdown.style.position = 'fixed';
        dropdown.style.top = (rect.bottom + 4) + 'px';
        dropdown.style.left = rect.left + 'px';
        dropdown.style.zIndex = '10001';
        document.body.appendChild(dropdown);
        activeSlingDropdown = dropdown;

        // Fetch rig options
        fetch('/api/options?type=rigs')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var rigs = data.rigs || [];
                if (rigs.length === 0) {
                    dropdown.innerHTML = '<div class="sling-dropdown-empty">No rigs available</div>';
                    return;
                }
                var html = '<div class="sling-dropdown-header">Sling ' + escapeHtml(beadId) + ' to:</div>';
                for (var i = 0; i < rigs.length; i++) {
                    html += '<button class="sling-dropdown-item" data-rig="' + escapeHtml(rigs[i]) + '">' + escapeHtml(rigs[i]) + '</button>';
                }
                dropdown.innerHTML = html;

                // Handle rig selection
                dropdown.addEventListener('click', function(e) {
                    var item = e.target.closest('.sling-dropdown-item');
                    if (!item) return;
                    var rig = item.getAttribute('data-rig');
                    closeSlingDropdown();
                    executeSling(beadId, rig);
                });
            })
            .catch(function() {
                dropdown.innerHTML = '<div class="sling-dropdown-empty">Failed to load rigs</div>';
            });
    }

    function executeSling(beadId, rig) {
        var cmd = 'sling ' + beadId + ' ' + rig;
        showToast('info', 'Slinging...', beadId + ' → ' + rig);

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Slung', beadId + ' → ' + rig);
                if (data.output && data.output.trim()) {
                    showOutput(cmd, data.output);
                }
            } else {
                showToast('error', 'Sling failed', data.error || 'Unknown error');
                if (data.output) {
                    showOutput(cmd, data.output);
                }
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
        });
    }

    // Click handler for sling buttons
    document.addEventListener('click', function(e) {
        var slingBtn = e.target.closest('.sling-btn');
        if (slingBtn) {
            e.preventDefault();
            e.stopPropagation();
            openSlingDropdown(slingBtn);
            return;
        }
        // Close dropdown when clicking outside
        if (activeSlingDropdown && !e.target.closest('.sling-dropdown')) {
            closeSlingDropdown();
        }
    });



})();
