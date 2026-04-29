// Dashboard module: issue-panel
// Issue panel interactions and action buttons (merged from the original
// dashboard.js ISSUE PANEL INTERACTIONS + ISSUE ACTION BUTTONS sections,
// lines 2200-2332 and 2336-2532). Merged because openIssueDetail (defined
// here) and renderIssueActions (defined further down) reference each other.
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

    var issuesList = document.getElementById('issues-list');
    var issueDetail = document.getElementById('issue-detail');
    var currentIssueId = null;

    // Click on issue row to view details
    document.addEventListener('click', function(e) {
        var issueRow = e.target.closest('.issue-row');
        if (issueRow && issueRow.hasAttribute('data-issue-id')) {
            e.preventDefault();
            var issueId = issueRow.getAttribute('data-issue-id');
            if (issueId) {
                openIssueDetail(issueId);
            }
        }

        // Click on dependency links
        var depItem = e.target.closest('.issue-dep-item');
        if (depItem) {
            e.preventDefault();
            var depId = depItem.getAttribute('data-issue-id');
            if (depId) {
                openIssueDetail(depId);
            }
        }
    });

    function openIssueDetail(issueId) {
        currentIssueId = issueId;

        // Pause HTMX refresh while viewing issue
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('issue-detail-id').textContent = issueId;
        document.getElementById('issue-detail-title-text').textContent = 'Loading...';
        document.getElementById('issue-detail-description').textContent = '';
        document.getElementById('issue-detail-priority').textContent = '';
        document.getElementById('issue-detail-status').textContent = '';
        document.getElementById('issue-detail-type').textContent = '';
        document.getElementById('issue-detail-created').textContent = '';
        document.getElementById('issue-detail-owner').textContent = '';
        document.getElementById('issue-detail-actions').innerHTML = '';
        document.getElementById('issue-detail-depends-on').innerHTML = '';
        document.getElementById('issue-detail-blocks').innerHTML = '';
        document.getElementById('issue-detail-deps').style.display = 'none';
        document.getElementById('issue-detail-blocks-section').style.display = 'none';

        // Show detail view
        issuesList.style.display = 'none';
        issueDetail.style.display = 'block';

        // Fetch issue details
        fetch('/api/issues/show?id=' + encodeURIComponent(issueId))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    document.getElementById('issue-detail-title-text').textContent = 'Error loading issue';
                    document.getElementById('issue-detail-description').textContent = data.error;
                    return;
                }

                document.getElementById('issue-detail-id').textContent = data.id || issueId;
                document.getElementById('issue-detail-title-text').textContent = data.title || '(no title)';
                document.getElementById('issue-detail-description').textContent = data.description || data.raw_output || '(no description)';

                // Priority badge
                var priorityEl = document.getElementById('issue-detail-priority');
                if (data.priority) {
                    priorityEl.textContent = data.priority;
                    priorityEl.className = 'badge';
                    if (data.priority === 'P1') priorityEl.classList.add('badge-red');
                    else if (data.priority === 'P2') priorityEl.classList.add('badge-orange');
                    else if (data.priority === 'P3') priorityEl.classList.add('badge-yellow');
                    else priorityEl.classList.add('badge-muted');
                }

                // Status
                var statusEl = document.getElementById('issue-detail-status');
                if (data.status) {
                    statusEl.textContent = data.status;
                    statusEl.className = 'issue-status ' + data.status.toLowerCase().replace(' ', '_');
                }

                // Meta info
                if (data.type) {
                    document.getElementById('issue-detail-type').textContent = 'Type: ' + data.type;
                }
                if (data.owner) {
                    document.getElementById('issue-detail-owner').textContent = 'Owner: ' + data.owner;
                }
                if (data.created) {
                    document.getElementById('issue-detail-created').textContent = 'Created: ' + data.created;
                }

                // Render action buttons
                renderIssueActions(issueId, data);

                // Dependencies
                if (data.depends_on && data.depends_on.length > 0) {
                    document.getElementById('issue-detail-deps').style.display = 'block';
                    var depsHtml = data.depends_on.map(function(dep) {
                        return '<span class="issue-dep-item" data-issue-id="' + escapeHtml(dep) + '">→ ' + escapeHtml(dep) + '</span>';
                    }).join(' ');
                    document.getElementById('issue-detail-depends-on').innerHTML = depsHtml;
                }

                // Blocks
                if (data.blocks && data.blocks.length > 0) {
                    document.getElementById('issue-detail-blocks-section').style.display = 'block';
                    var blocksHtml = data.blocks.map(function(dep) {
                        return '<span class="issue-dep-item" data-issue-id="' + escapeHtml(dep) + '">← ' + escapeHtml(dep) + '</span>';
                    }).join(' ');
                    document.getElementById('issue-detail-blocks').innerHTML = blocksHtml;
                }
            })
            .catch(function(err) {
                document.getElementById('issue-detail-title-text').textContent = 'Error';
                document.getElementById('issue-detail-description').textContent = 'Failed to load issue: ' + err.message;
            });
    }

    // Back button from issue detail
    var issueBackBtn = document.getElementById('issue-back-btn');
    if (issueBackBtn) {
        issueBackBtn.addEventListener('click', function() {
            issueDetail.style.display = 'none';
            issuesList.style.display = 'block';
            currentIssueId = null;
            // Resume HTMX refresh
            window.pauseRefresh = false;
        });
    }

    // Render action buttons based on current issue state
    function renderIssueActions(issueId, data) {
        var actionsEl = document.getElementById('issue-detail-actions');
        if (!actionsEl) return;

        var status = (data.status || '').toUpperCase();
        var isClosed = status === 'CLOSED';
        var currentPriority = data.priority || 'P2';
        // Extract numeric priority (P1 -> 1, P2 -> 2, etc.)
        var priNum = currentPriority.length === 2 ? parseInt(currentPriority[1], 10) : 2;

        var html = '<div class="issue-actions-bar">';

        // Close / Reopen button
        if (isClosed) {
            html += '<button class="issue-action-btn reopen" onclick="reopenIssue(\'' + escapeHtml(issueId) + '\')">↺ Reopen</button>';
        } else {
            html += '<button class="issue-action-btn close" onclick="closeIssue(\'' + escapeHtml(issueId) + '\')">✓ Close</button>';
        }

        // Priority dropdown
        html += '<div class="issue-action-group">';
        html += '<label class="issue-action-label">Priority</label>';
        html += '<select class="issue-action-select" id="issue-action-priority" onchange="updateIssuePriority(\'' + escapeHtml(issueId) + '\', this.value)">';
        for (var p = 1; p <= 4; p++) {
            var sel = p === priNum ? ' selected' : '';
            var pLabel = p === 1 ? 'P1 - Critical' : p === 2 ? 'P2 - High' : p === 3 ? 'P3 - Medium' : 'P4 - Low';
            html += '<option value="' + p + '"' + sel + '>' + pLabel + '</option>';
        }
        html += '</select>';
        html += '</div>';

        // Assignee dropdown
        html += '<div class="issue-action-group">';
        html += '<label class="issue-action-label">Assign</label>';
        html += '<select class="issue-action-select" id="issue-action-assignee" onchange="assignIssue(\'' + escapeHtml(issueId) + '\', this.value)">';
        html += '<option value="">Unassigned</option>';
        html += '<option value="" disabled>Loading agents...</option>';
        html += '</select>';
        html += '</div>';

        html += '</div>';
        actionsEl.innerHTML = html;

        // Load agents for assignee dropdown
        loadAssigneeOptions(data.owner || '');
    }

    // Load agent options into the assignee dropdown
    function loadAssigneeOptions(currentOwner) {
        var select = document.getElementById('issue-action-assignee');
        if (!select) return;

        fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                // Rebuild dropdown
                var html = '<option value="">Unassigned</option>';
                var agents = data.agents || [];
                var polecats = data.polecats || [];

                // Combine agents and polecats for assignee options
                var seen = {};
                var allOptions = [];

                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    if (!seen[name]) {
                        seen[name] = true;
                        allOptions.push(name);
                    }
                });

                polecats.forEach(function(polecat) {
                    if (!seen[polecat]) {
                        seen[polecat] = true;
                        allOptions.push(polecat);
                    }
                });

                allOptions.forEach(function(name) {
                    var sel = name === currentOwner ? ' selected' : '';
                    html += '<option value="' + escapeHtml(name) + '"' + sel + '>' + escapeHtml(name) + '</option>';
                });

                select.innerHTML = html;
            })
            .catch(function() {
                select.innerHTML = '<option value="">Unassigned</option>';
            });
    }

    // Close an issue
    function closeIssue(issueId) {
        if (!confirm('Close issue ' + issueId + '?')) return;

        showToast('info', 'Closing...', issueId);

        fetch('/api/issues/close', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Closed', issueId + ' closed');
                // Re-fetch to update the detail view
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.closeIssue = closeIssue;

    // Reopen an issue
    function reopenIssue(issueId) {
        showToast('info', 'Reopening...', issueId);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, status: 'open' })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Reopened', issueId + ' reopened');
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.reopenIssue = reopenIssue;

    // Update issue priority
    function updateIssuePriority(issueId, priority) {
        var priNum = parseInt(priority, 10);
        if (priNum < 1 || priNum > 4) return;

        showToast('info', 'Updating...', 'Setting priority to P' + priNum);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, priority: priNum })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Updated', 'Priority set to P' + priNum);
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.updateIssuePriority = updateIssuePriority;

    // Assign issue to agent
    function assignIssue(issueId, assignee) {
        if (!assignee) return; // Unassigned selected, no-op for now

        showToast('info', 'Assigning...', 'Assigning to ' + assignee);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, assignee: assignee })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Assigned', 'Assigned to ' + assignee);
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.assignIssue = assignIssue;

})();
