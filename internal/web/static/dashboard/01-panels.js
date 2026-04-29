// Dashboard module: panels
// Panel expand/collapse handlers and HTMX after-swap hook.
// Depends on: window.gtDash (updateConnectionStatus)
(function() {
    'use strict';

    var gtDash = window.gtDash || {};

    // ============================================
    // EXPAND BUTTON HANDLER
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.expand-btn');
        if (!btn) return;

        e.preventDefault();
        var panel = btn.closest('.panel');
        if (!panel) return;

        if (panel.classList.contains('expanded')) {
            panel.classList.remove('expanded');
            btn.textContent = 'Expand';
            // Resume refresh when panel is collapsed
            window.pauseRefresh = false;
        } else {
            document.querySelectorAll('.panel.expanded').forEach(function(p) {
                p.classList.remove('expanded');
                var b = p.querySelector('.expand-btn');
                if (b) b.textContent = 'Expand';
            });
            panel.classList.add('expanded');
            btn.textContent = '✕ Close';
            // Pause refresh while panel is expanded
            window.pauseRefresh = true;
        }
    });

    // ============================================
    // COLLAPSE BUTTON HANDLER
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.collapse-btn');
        if (!btn) return;

        e.preventDefault();
        var panel = btn.closest('.panel');
        if (!panel) return;

        panel.classList.toggle('collapsed');
    });

    // After HTMX swap - morph preserves most state, but we need to re-init some things
    document.body.addEventListener('htmx:afterSwap', function() {
        // Morph preserves expanded class, so we don't need to close panels anymore
        // Just check if we should resume refresh
        var hasExpanded = document.querySelector('.panel.expanded');
        var mailDetail = document.getElementById('mail-detail');
        var mailCompose = document.getElementById('mail-compose');
        var issueDetail = document.getElementById('issue-detail');
        var prDetail = document.getElementById('pr-detail');
        var convoyDetailView = document.getElementById('convoy-detail');
        var convoyCreateView = document.getElementById('convoy-create-form');
        var sessionPreview = document.getElementById('session-preview');
        var inDetailView = (mailDetail && mailDetail.style.display !== 'none') ||
                          (mailCompose && mailCompose.style.display !== 'none') ||
                          (issueDetail && issueDetail.style.display !== 'none') ||
                          (prDetail && prDetail.style.display !== 'none') ||
                          (convoyDetailView && convoyDetailView.style.display !== 'none') ||
                          (convoyCreateView && convoyCreateView.style.display !== 'none') ||
                          (sessionPreview && sessionPreview.style.display !== 'none');
        if (!inDetailView && !hasExpanded) {
            window.pauseRefresh = false;
        }
        // Reload dynamic panels after swap (handled via window functions)
        if (window.refreshCrewPanel) window.refreshCrewPanel();
        if (window.refreshReadyPanel) window.refreshReadyPanel();
        // Update connection status indicator after morph
        if (gtDash.updateConnectionStatus) {
            gtDash.updateConnectionStatus(window.sseConnected ? 'live' : 'reconnecting');
        }
    });
})();
