// dashboard/mergeQueue.js
// PR / merge queue panel interactions.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // PR/MERGE QUEUE PANEL INTERACTIONS
    // ============================================
    var prList = document.getElementById('pr-list');
    var prDetail = document.getElementById('pr-detail');
    var currentPrUrl = null;

    // Click on PR row to view details
    document.addEventListener('click', function(e) {
        var prRow = e.target.closest('.pr-row');
        if (prRow && prRow.hasAttribute('data-pr-url')) {
            e.preventDefault();
            var prUrl = prRow.getAttribute('data-pr-url');
            if (prUrl) {
                openPrDetail(prUrl);
            }
        }
    });

    function openPrDetail(prUrl) {
        currentPrUrl = prUrl;

        // Pause HTMX refresh while viewing PR
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('pr-detail-number').textContent = 'Loading...';
        document.getElementById('pr-detail-title-text').textContent = '';
        document.getElementById('pr-detail-body').textContent = '';
        document.getElementById('pr-detail-state').textContent = '';
        document.getElementById('pr-detail-author').textContent = '';
        document.getElementById('pr-detail-branches').textContent = '';
        document.getElementById('pr-detail-created').textContent = '';
        document.getElementById('pr-detail-additions').textContent = '';
        document.getElementById('pr-detail-deletions').textContent = '';
        document.getElementById('pr-detail-files').textContent = '';
        document.getElementById('pr-detail-labels').innerHTML = '';
        document.getElementById('pr-detail-checks').innerHTML = '';
        document.getElementById('pr-detail-labels-section').style.display = 'none';
        document.getElementById('pr-detail-checks-section').style.display = 'none';
        document.getElementById('pr-detail-link').href = prUrl;

        // Show detail view
        prList.style.display = 'none';
        prDetail.style.display = 'block';

        // Fetch PR details
        fetch('/api/pr/show?url=' + encodeURIComponent(prUrl))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    document.getElementById('pr-detail-title-text').textContent = 'Error loading PR';
                    document.getElementById('pr-detail-body').textContent = data.error;
                    return;
                }

                document.getElementById('pr-detail-number').textContent = '#' + data.number;
                document.getElementById('pr-detail-title-text').textContent = data.title || '(no title)';
                document.getElementById('pr-detail-body').textContent = data.body || '(no description)';

                // State badge
                var stateEl = document.getElementById('pr-detail-state');
                if (data.state) {
                    stateEl.textContent = data.state;
                    stateEl.className = 'pr-state ' + data.state.toLowerCase();
                }

                // Meta info
                if (data.author) {
                    document.getElementById('pr-detail-author').textContent = 'by ' + data.author;
                }
                if (data.base_ref && data.head_ref) {
                    document.getElementById('pr-detail-branches').textContent = data.head_ref + ' → ' + data.base_ref;
                }
                if (data.created_at) {
                    var created = new Date(data.created_at);
                    document.getElementById('pr-detail-created').textContent = 'Created ' + created.toLocaleDateString();
                }

                // Stats
                if (data.additions !== undefined) {
                    document.getElementById('pr-detail-additions').textContent = '+' + data.additions;
                }
                if (data.deletions !== undefined) {
                    document.getElementById('pr-detail-deletions').textContent = '-' + data.deletions;
                }
                if (data.changed_files !== undefined) {
                    document.getElementById('pr-detail-files').textContent = data.changed_files + ' files';
                }

                // Labels
                if (data.labels && data.labels.length > 0) {
                    document.getElementById('pr-detail-labels-section').style.display = 'block';
                    var labelsHtml = data.labels.map(function(label) {
                        return '<span class="pr-label">' + escapeHtml(label) + '</span>';
                    }).join(' ');
                    document.getElementById('pr-detail-labels').innerHTML = labelsHtml;
                }

                // Checks
                if (data.checks && data.checks.length > 0) {
                    document.getElementById('pr-detail-checks-section').style.display = 'block';
                    var checksHtml = data.checks.map(function(check) {
                        var checkClass = 'pr-check';
                        if (check.toLowerCase().includes('success')) checkClass += ' success';
                        else if (check.toLowerCase().includes('failure')) checkClass += ' failure';
                        else if (check.toLowerCase().includes('pending') || check.toLowerCase().includes('in_progress')) checkClass += ' pending';
                        return '<span class="' + checkClass + '">' + escapeHtml(check) + '</span>';
                    }).join('');
                    document.getElementById('pr-detail-checks').innerHTML = checksHtml;
                }
            })
            .catch(function(err) {
                document.getElementById('pr-detail-title-text').textContent = 'Error';
                document.getElementById('pr-detail-body').textContent = 'Failed to load PR: ' + err.message;
            });
    }

    // Back button from PR detail
    var prBackBtn = document.getElementById('pr-back-btn');
    if (prBackBtn) {
        prBackBtn.addEventListener('click', function() {
            prDetail.style.display = 'none';
            prList.style.display = 'block';
            currentPrUrl = null;
            // Resume HTMX refresh
            window.pauseRefresh = false;
        });
    }


})();
