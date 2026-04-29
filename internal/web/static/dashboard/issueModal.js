// dashboard/issueModal.js
// Issue creation modal.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // ISSUE CREATION MODAL
    // ============================================
    function openIssueModal() {
        var modal = document.getElementById('issue-modal');
        if (modal) {
            modal.style.display = 'flex';
            window.pauseRefresh = true;
            // Focus the title input
            var titleInput = document.getElementById('issue-title');
            if (titleInput) {
                setTimeout(function() { titleInput.focus(); }, 100);
            }
        }
    }
    window.openIssueModal = openIssueModal;

    function closeIssueModal() {
        var modal = document.getElementById('issue-modal');
        if (modal) {
            modal.style.display = 'none';
            window.pauseRefresh = false;
            // Reset form
            var form = document.getElementById('issue-form');
            if (form) form.reset();
        }
    }
    window.closeIssueModal = closeIssueModal;

    function submitIssue(e) {
        e.preventDefault();
        
        var title = document.getElementById('issue-title').value.trim();
        var priority = document.getElementById('issue-priority').value;
        var description = document.getElementById('issue-description').value.trim();
        var submitBtn = document.getElementById('issue-submit-btn');

        if (!title) {
            showToast('error', 'Missing', 'Title is required');
            return;
        }

        // Disable button while submitting
        submitBtn.disabled = true;
        submitBtn.textContent = 'Creating...';

        var payload = {
            title: title,
            priority: parseInt(priority, 10)
        };
        if (description) {
            payload.description = description;
        }

        fetch('/api/issues/create', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Created', 'Issue ' + (data.id || '') + ' created');
                closeIssueModal();
                // Trigger a page refresh to show the new issue
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            submitBtn.disabled = false;
            submitBtn.textContent = 'Create Issue';
        });
    }
    window.submitIssue = submitIssue;

    // Close modal on Escape key
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') {
            var modal = document.getElementById('issue-modal');
            if (modal && modal.style.display !== 'none') {
                closeIssueModal();
            }
        }
    });


})();
