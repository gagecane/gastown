// dashboard/convoyDrilldown.js
// Convoy drill-down rows: expand to show tracked issues.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // CONVOY DRILL-DOWN (expand rows to show tracked issues)
    // ============================================
    var convoyCache = {}; // Cache fetched convoy data by ID

    document.addEventListener('click', function(e) {
        var row = e.target.closest('.convoy-row');
        if (!row) return;

        e.preventDefault();
        var convoyId = row.getAttribute('data-convoy-id');
        if (!convoyId) return;

        // Check if already expanded
        var existingDetail = row.nextElementSibling;
        if (existingDetail && existingDetail.classList.contains('convoy-detail-row')) {
            // Collapse: remove the detail row
            existingDetail.remove();
            row.classList.remove('convoy-expanded');
            var toggle = row.querySelector('.convoy-toggle');
            if (toggle) toggle.textContent = '▶';
            return;
        }

        // Collapse any other expanded convoy
        document.querySelectorAll('.convoy-detail-row').forEach(function(r) { r.remove(); });
        document.querySelectorAll('.convoy-row.convoy-expanded').forEach(function(r) {
            r.classList.remove('convoy-expanded');
            var t = r.querySelector('.convoy-toggle');
            if (t) t.textContent = '▶';
        });

        // Mark this row as expanded
        row.classList.add('convoy-expanded');
        var toggleEl = row.querySelector('.convoy-toggle');
        if (toggleEl) toggleEl.textContent = '▼';

        // Create detail row
        var detailRow = document.createElement('tr');
        detailRow.className = 'convoy-detail-row';
        var detailCell = document.createElement('td');
        detailCell.colSpan = 4;
        detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-loading">Loading tracked issues...</div></div>';
        detailRow.appendChild(detailCell);
        row.parentNode.insertBefore(detailRow, row.nextSibling);

        // Check cache first
        if (convoyCache[convoyId]) {
            renderConvoyIssues(detailCell, convoyCache[convoyId]);
            return;
        }

        // Fetch via /api/run
        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'convoy status ' + convoyId + ' --json' })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.success) {
                detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Failed to load: ' + escapeHtml(data.error || 'Unknown error') + '</div></div>';
                return;
            }
            try {
                var parsed = JSON.parse(data.output);
                convoyCache[convoyId] = parsed;
                renderConvoyIssues(detailCell, parsed);
            } catch (err) {
                detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Failed to parse response</div></div>';
            }
        })
        .catch(function(err) {
            detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Request failed: ' + escapeHtml(err.message) + '</div></div>';
        });
    });

    function renderConvoyIssues(cell, data) {
        var issues = data.tracked || [];
        if (issues.length === 0) {
            cell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-empty">No tracked issues</div></div>';
            return;
        }

        var html = '<div class="tracked-issues">';
        html += '<table class="tracked-issues-table">';
        html += '<thead><tr><th>Status</th><th>ID</th><th>Title</th><th>Assignee</th><th>Progress</th></tr></thead>';
        html += '<tbody>';

        for (var i = 0; i < issues.length; i++) {
            var issue = issues[i];

            // Status badge
            var statusBadge = '';
            switch (issue.status) {
                case 'closed':
                    statusBadge = '<span class="badge badge-green">Done</span>';
                    break;
                case 'in_progress':
                    statusBadge = '<span class="badge badge-yellow">In Progress</span>';
                    break;
                case 'hooked':
                    statusBadge = '<span class="badge badge-blue">Hooked</span>';
                    break;
                default:
                    statusBadge = '<span class="badge badge-muted">Open</span>';
            }

            // Assignee - extract short name
            var assignee = '—';
            if (issue.assignee) {
                var parts = issue.assignee.split('/');
                assignee = parts[parts.length - 1];
            }

            // Worker info as progress indicator
            var progress = '';
            if (issue.status === 'closed') {
                progress = '<span class="convoy-progress-done">✓</span>';
            } else if (issue.worker) {
                var workerName = issue.worker.split('/').pop();
                progress = '<span class="convoy-progress-active">@' + escapeHtml(workerName) + '</span>';
                if (issue.worker_age) {
                    progress += ' <span class="convoy-progress-age">' + escapeHtml(issue.worker_age) + '</span>';
                }
            }

            html += '<tr class="tracked-issue-row tracked-issue-' + escapeHtml(issue.status) + '">' +
                '<td>' + statusBadge + '</td>' +
                '<td><span class="issue-id">' + escapeHtml(issue.id) + '</span></td>' +
                '<td class="tracked-issue-title">' + escapeHtml(issue.title) + '</td>' +
                '<td class="tracked-issue-assignee">' + escapeHtml(assignee) + '</td>' +
                '<td class="tracked-issue-progress">' + progress + '</td>' +
                '</tr>';
        }

        html += '</tbody></table>';

        // Progress summary
        var completed = data.completed || 0;
        var total = data.total || issues.length;
        var pct = total > 0 ? Math.round((completed / total) * 100) : 0;
        html += '<div class="tracked-issues-summary">';
        html += '<div class="tracked-issues-progress-bar"><div class="tracked-issues-progress-fill" style="width: ' + pct + '%;"></div></div>';
        html += '<span class="tracked-issues-progress-text">' + completed + '/' + total + ' completed (' + pct + '%)</span>';
        html += '</div>';

        html += '</div>';
        cell.innerHTML = html;
    }

})();
