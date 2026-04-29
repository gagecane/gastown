// dashboard/issuePanel.js
// Issue panel: list, filters, detail view interactions.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // ISSUE PANEL INTERACTIONS
    // ============================================
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
                if (window.renderIssueActions) window.renderIssueActions(issueId, data);

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

    // Expose for cross-module use (issueActions reopens detail view after mutations).
    window.openIssueDetail = openIssueDetail;

})();
