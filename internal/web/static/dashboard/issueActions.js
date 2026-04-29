// dashboard/issueActions.js
// Issue action buttons: close, reopen, priority, assign.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // ISSUE ACTION BUTTONS
    // ============================================

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
                if (window.openIssueDetail) window.openIssueDetail(issueId);
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
                if (window.openIssueDetail) window.openIssueDetail(issueId);
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
                if (window.openIssueDetail) window.openIssueDetail(issueId);
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
                if (window.openIssueDetail) window.openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.assignIssue = assignIssue;

    // Expose for issuePanel (which renders actions after fetching issue detail).
    window.renderIssueActions = renderIssueActions;

})();
