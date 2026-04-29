// Dashboard module: convoy-panel
// Extracted from dashboard.js lines 1607-2196.
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

    var convoyList = document.getElementById('convoy-list');
    var convoyDetail = document.getElementById('convoy-detail');
    var convoyCreateForm = document.getElementById('convoy-create-form');
    var currentConvoyId = null;

    // Click on convoy row to view details
    document.addEventListener('click', function(e) {
        var convoyRow = e.target.closest('.convoy-row');
        if (convoyRow && convoyRow.hasAttribute('data-convoy-id')) {
            e.preventDefault();
            var convoyId = convoyRow.getAttribute('data-convoy-id');
            if (convoyId) {
                openConvoyDetail(convoyId);
            }
        }
    });

    function openConvoyDetail(convoyId) {
        currentConvoyId = convoyId;
        window.pauseRefresh = true;

        // Reset views
        document.getElementById('convoy-detail-id').textContent = convoyId;
        document.getElementById('convoy-detail-title').textContent = 'Convoy: ' + convoyId;
        document.getElementById('convoy-detail-status').textContent = '';
        document.getElementById('convoy-detail-progress').textContent = '';
        document.getElementById('convoy-issues-loading').style.display = 'block';
        document.getElementById('convoy-issues-table').style.display = 'none';
        document.getElementById('convoy-issues-empty').style.display = 'none';
        document.getElementById('convoy-add-issue-form').style.display = 'none';

        // Show detail, hide list and create form
        convoyList.style.display = 'none';
        convoyCreateForm.style.display = 'none';
        convoyDetail.style.display = 'block';

        // Fetch convoy status via /api/run
        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'convoy status ' + convoyId })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            document.getElementById('convoy-issues-loading').style.display = 'none';

            if (!data.success) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                document.getElementById('convoy-issues-empty').querySelector('p').textContent = data.error || 'Failed to load convoy';
                return;
            }

            var issues = parseConvoyStatusOutput(data.output || '');
            if (issues.length === 0) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                return;
            }

            var tbody = document.getElementById('convoy-issues-tbody');
            tbody.innerHTML = '';
            issues.forEach(function(issue) {
                var tr = document.createElement('tr');
                var statusBadge = '';
                var statusLower = (issue.status || '').toLowerCase();
                if (statusLower === 'closed' || statusLower === 'complete' || statusLower === 'done') {
                    statusBadge = '<span class="badge badge-green">Done</span>';
                } else if (statusLower === 'in_progress' || statusLower === 'in progress' || statusLower === 'working') {
                    statusBadge = '<span class="badge badge-yellow">In Progress</span>';
                } else if (statusLower === 'open' || statusLower === 'ready') {
                    statusBadge = '<span class="badge badge-blue">Open</span>';
                } else if (statusLower === 'blocked') {
                    statusBadge = '<span class="badge badge-red">Blocked</span>';
                } else {
                    statusBadge = '<span class="badge badge-muted">' + escapeHtml(issue.status || 'Unknown') + '</span>';
                }

                tr.innerHTML =
                    '<td class="convoy-issue-status">' + statusBadge + '</td>' +
                    '<td><span class="issue-id">' + escapeHtml(issue.id) + '</span></td>' +
                    '<td class="issue-title">' + escapeHtml(issue.title || '') + '</td>' +
                    '<td>' + (issue.assignee ? '<span class="badge badge-blue">' + escapeHtml(issue.assignee) + '</span>' : '<span class="badge badge-muted">Unassigned</span>') + '</td>' +
                    '<td>' + escapeHtml(issue.progress || '') + '</td>';
                tbody.appendChild(tr);
            });
            document.getElementById('convoy-issues-table').style.display = 'table';
        })
        .catch(function(err) {
            document.getElementById('convoy-issues-loading').style.display = 'none';
            document.getElementById('convoy-issues-empty').style.display = 'block';
            document.getElementById('convoy-issues-empty').querySelector('p').textContent = 'Error: ' + err.message;
        });
    }

    // Parse convoy status text output into issue objects
    function parseConvoyStatusOutput(output) {
        var issues = [];
        var lines = output.split('\n');
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i].trim();
            if (!line) continue;
            // Skip header lines and convoy summary lines
            if (line.startsWith('Convoy') || line.startsWith('===') || line.startsWith('---') ||
                line.startsWith('Status:') || line.startsWith('Progress:') || line.startsWith('Created:') ||
                line.startsWith('Title:') || line.startsWith('Issues:') || line.startsWith('Name:')) {
                // Extract convoy-level status/progress for the detail header
                if (line.startsWith('Status:')) {
                    var statusEl = document.getElementById('convoy-detail-status');
                    var statusVal = line.replace('Status:', '').trim().toLowerCase();
                    statusEl.textContent = statusVal;
                    statusEl.className = 'badge';
                    if (statusVal === 'active') statusEl.classList.add('badge-green');
                    else if (statusVal === 'stale') statusEl.classList.add('badge-yellow');
                    else if (statusVal === 'stuck') statusEl.classList.add('badge-red');
                    else if (statusVal === 'complete') statusEl.classList.add('badge-green');
                    else statusEl.classList.add('badge-muted');
                }
                if (line.startsWith('Progress:')) {
                    document.getElementById('convoy-detail-progress').textContent = line.replace('Progress:', '').trim();
                }
                continue;
            }
            // Look for issue lines - typically formatted as:
            // "○ id · title [● P2 · STATUS]" or similar bead-style output
            // Or tabular: "id   title   status   assignee"
            var issue = parseConvoyIssueLine(line);
            if (issue) {
                issues.push(issue);
            }
        }
        return issues;
    }

    // Parse a single issue line from convoy status output
    function parseConvoyIssueLine(line) {
        // Try bead-style format: "○ id · title   [● P2 · OPEN]"
        // or "◐ id · title   [● P2 · IN_PROGRESS]"
        var beadMatch = line.match(/^[○◐●✓]\s+(\S+)\s+[·:]\s+(.+?)(?:\s+\[.*?([A-Z_]+)\])?$/);
        if (beadMatch) {
            var statusFromBracket = '';
            if (beadMatch[3]) {
                statusFromBracket = beadMatch[3].toLowerCase().replace('_', ' ');
            } else {
                // Infer from icon
                if (line.startsWith('✓')) statusFromBracket = 'closed';
                else if (line.startsWith('◐')) statusFromBracket = 'in progress';
                else statusFromBracket = 'open';
            }
            return {
                id: beadMatch[1],
                title: beadMatch[2].trim(),
                status: statusFromBracket,
                assignee: '',
                progress: ''
            };
        }

        // Try simple "id title" format (at least an ID-like token)
        var parts = line.split(/\s{2,}/);
        if (parts.length >= 2 && parts[0].match(/^[a-zA-Z0-9_-]+$/)) {
            return {
                id: parts[0],
                title: parts[1] || '',
                status: parts[2] || '',
                assignee: parts[3] || '',
                progress: parts[4] || ''
            };
        }

        return null;
    }

    // Back button from convoy detail
    document.getElementById('convoy-back-btn').addEventListener('click', function() {
        convoyDetail.style.display = 'none';
        convoyList.style.display = 'block';
        currentConvoyId = null;
        window.pauseRefresh = false;
    });

    // New Convoy button
    document.getElementById('new-convoy-btn').addEventListener('click', function() {
        window.pauseRefresh = true;
        convoyList.style.display = 'none';
        convoyDetail.style.display = 'none';
        convoyCreateForm.style.display = 'block';
        document.getElementById('convoy-create-name').value = '';
        document.getElementById('convoy-create-issues').value = '';
        document.getElementById('convoy-create-name').focus();
    });

    // Cancel create convoy
    document.getElementById('convoy-create-back-btn').addEventListener('click', cancelConvoyCreate);
    document.getElementById('convoy-create-cancel-btn').addEventListener('click', cancelConvoyCreate);

    function cancelConvoyCreate() {
        convoyCreateForm.style.display = 'none';
        convoyList.style.display = 'block';
        window.pauseRefresh = false;
    }

    // Submit create convoy
    document.getElementById('convoy-create-submit-btn').addEventListener('click', function() {
        var name = document.getElementById('convoy-create-name').value.trim();
        var issuesStr = document.getElementById('convoy-create-issues').value.trim();

        if (!name) {
            showToast('error', 'Missing', 'Convoy name is required');
            return;
        }

        var btn = document.getElementById('convoy-create-submit-btn');
        btn.disabled = true;
        btn.textContent = 'Creating...';

        // Build command: convoy create <name> [issue1 issue2 ...]
        var cmd = 'convoy create ' + name;
        if (issuesStr) {
            cmd += ' ' + issuesStr;
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Created', 'Convoy "' + name + '" created');
                cancelConvoyCreate();
                if (data.output && data.output.trim()) {
                    showOutput(cmd, data.output);
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Create Convoy';
        });
    });

    // Add Issue button in convoy detail
    document.getElementById('convoy-add-issue-btn').addEventListener('click', function() {
        var form = document.getElementById('convoy-add-issue-form');
        form.style.display = form.style.display === 'none' ? 'flex' : 'none';
        if (form.style.display !== 'none') {
            document.getElementById('convoy-add-issue-input').value = '';
            document.getElementById('convoy-add-issue-input').focus();
        }
    });

    // Cancel add issue
    document.getElementById('convoy-add-issue-cancel').addEventListener('click', function() {
        document.getElementById('convoy-add-issue-form').style.display = 'none';
    });

    // Submit add issue to convoy
    document.getElementById('convoy-add-issue-submit').addEventListener('click', submitAddIssueToConvoy);

    // Enter key in add issue input
    document.getElementById('convoy-add-issue-input').addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            submitAddIssueToConvoy();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            document.getElementById('convoy-add-issue-form').style.display = 'none';
        }
    });

    function submitAddIssueToConvoy() {
        var issueId = document.getElementById('convoy-add-issue-input').value.trim();
        if (!issueId || !currentConvoyId) {
            showToast('error', 'Missing', 'Issue ID is required');
            return;
        }

        var btn = document.getElementById('convoy-add-issue-submit');
        btn.disabled = true;
        btn.textContent = 'Adding...';

        var cmd = 'convoy add ' + currentConvoyId + ' ' + issueId;

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Added', 'Issue ' + issueId + ' added to convoy');
                document.getElementById('convoy-add-issue-form').style.display = 'none';
                // Refresh the convoy detail view
                openConvoyDetail(currentConvoyId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Add';
        });
    }


})();
