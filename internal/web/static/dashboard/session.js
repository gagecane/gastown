// dashboard/session.js
// Session terminal preview.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // SESSION TERMINAL PREVIEW
    // ============================================
    var sessionPreviewInterval = null;
    var sessionsTable = null; // will be set when opening preview

    // Click on session row to preview terminal output
    document.addEventListener('click', function(e) {
        var sessionRow = e.target.closest('.session-row');
        if (sessionRow) {
            e.preventDefault();
            var sessionName = sessionRow.getAttribute('data-session-name');
            if (sessionName) {
                openSessionPreview(sessionName);
            }
        }
    });

    function openSessionPreview(sessionName) {
        window.pauseRefresh = true;

        var preview = document.getElementById('session-preview');
        var nameEl = document.getElementById('session-preview-name');
        var contentEl = document.getElementById('session-preview-content');
        var statusEl = document.getElementById('session-preview-status');

        if (!preview || !contentEl) return;

        // Hide the sessions table, show preview
        sessionsTable = preview.parentNode.querySelector('table');
        if (sessionsTable) sessionsTable.style.display = 'none';
        var emptyState = preview.parentNode.querySelector('.empty-state');
        if (emptyState) emptyState.style.display = 'none';

        nameEl.textContent = sessionName;
        contentEl.textContent = 'Loading...';
        statusEl.textContent = '';
        preview.style.display = 'block';

        // Fetch immediately
        fetchSessionPreview(sessionName, contentEl, statusEl);

        // Auto-refresh every 3 seconds
        if (sessionPreviewInterval) clearInterval(sessionPreviewInterval);
        sessionPreviewInterval = setInterval(function() {
            fetchSessionPreview(sessionName, contentEl, statusEl);
        }, 250);
    }

    function fetchSessionPreview(sessionName, contentEl, statusEl) {
        fetch('/api/session/preview?session=' + encodeURIComponent(sessionName))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    contentEl.textContent = 'Error: ' + data.error;
                    return;
                }
                var newText = data.content || '(empty)';
                if (newText !== (contentEl._rawContent || '')) {
                    contentEl._rawContent = newText;
                    var atBottom = contentEl.scrollHeight - contentEl.scrollTop - contentEl.clientHeight < 20;
                    var savedScroll = contentEl.scrollTop;
                    contentEl.innerHTML = ansiToHtml(newText);
                    if (atBottom) {
                        contentEl.scrollTop = contentEl.scrollHeight;
                    } else {
                        contentEl.scrollTop = savedScroll;
                    }
                }
                var now = new Date();
                var timeStr = now.getHours() + ':' + (now.getMinutes() < 10 ? '0' : '') + now.getMinutes() + ':' + (now.getSeconds() < 10 ? '0' : '') + now.getSeconds();
                statusEl.textContent = 'refreshed ' + timeStr;
            })
            .catch(function(err) {
                contentEl.textContent = 'Failed to load preview: ' + err.message;
            });
    }

    function closeSessionPreview() {
        if (sessionPreviewInterval) {
            clearInterval(sessionPreviewInterval);
            sessionPreviewInterval = null;
        }

        var preview = document.getElementById('session-preview');
        if (preview) preview.style.display = 'none';

        // Show the sessions table again
        if (sessionsTable) sessionsTable.style.display = '';

        window.pauseRefresh = false;
    }

    // Back button from session preview
    var sessionPreviewBack = document.getElementById('session-preview-back');
    if (sessionPreviewBack) {
        sessionPreviewBack.addEventListener('click', closeSessionPreview);
    }

    // Session input send
    var sessionSendBtn = document.getElementById('session-send-btn');
    var sessionSendInput = document.getElementById('session-send-input');
    function sendSessionInput() {
        var nameEl = document.getElementById('session-preview-name');
        var input = sessionSendInput ? sessionSendInput.value.trim() : '';
        if (!input || !nameEl) return;
        fetch('/api/session/send', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({session: nameEl.textContent, input: input})
        }).then(function() {
            sessionSendInput.value = '';
            var contentEl = document.getElementById('session-preview-content');
            var statusEl = document.getElementById('session-preview-status');
            setTimeout(function() { fetchSessionPreview(nameEl.textContent, contentEl, statusEl); }, 500);
        });
    }
    if (sessionSendBtn) sessionSendBtn.addEventListener('click', sendSessionInput);
    if (sessionSendInput) sessionSendInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') sendSessionInput();
    });

    // Hotkey buttons
    document.querySelectorAll('.session-hotkey').forEach(function(btn) {
        btn.addEventListener('click', function() {
            var nameEl = document.getElementById('session-preview-name');
            if (!nameEl) return;
            fetch('/api/session/send', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({session: nameEl.textContent, input: btn.getAttribute('data-key')})
            }).then(function() {
                var contentEl = document.getElementById('session-preview-content');
                var statusEl = document.getElementById('session-preview-status');
                setTimeout(function() { fetchSessionPreview(nameEl.textContent, contentEl, statusEl); }, 300);
            });
        });
    });


})();
