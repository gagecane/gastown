(function() {
    'use strict';

    // Shared namespace for cross-module helpers.
    // Loaded first; other modules access helpers via `window.GT.escapeHtml` etc.
    window.GT = window.GT || {};

    // ============================================
    // SHARED HELPERS (escapeHtml / ansiToHtml / showToast)
    // ============================================

    function escapeHtml(str) {
        if (!str) return '';
        var div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // Convert ANSI escape sequences to HTML spans with CSS classes.
    function ansiToHtml(str) {
        if (!str) return '';
        var out = '';
        var open = false;
        var i = 0;
        while (i < str.length) {
            if (str.charCodeAt(i) === 0x1b && str[i + 1] === '[') {
                var end = str.indexOf('m', i + 2);
                if (end === -1) { i++; continue; }
                var codes = str.substring(i + 2, end).split(';');
                i = end + 1;
                if (open) { out += '</span>'; open = false; }
                var classes = [];
                for (var c = 0; c < codes.length; c++) {
                    var n = parseInt(codes[c], 10);
                    if (n === 0) { /* reset */ }
                    else if (n === 1) classes.push('ansi-bold');
                    else if (n === 2) classes.push('ansi-dim');
                    else if (n === 3) classes.push('ansi-italic');
                    else if (n === 4) classes.push('ansi-underline');
                    else if (n >= 30 && n <= 37) classes.push('ansi-fg-' + (n - 30));
                    else if (n >= 40 && n <= 47) classes.push('ansi-bg-' + (n - 40));
                    else if (n >= 90 && n <= 97) classes.push('ansi-fg-' + (n - 90) + '-bright');
                    else if (n >= 100 && n <= 107) classes.push('ansi-bg-' + (n - 100) + '-bright');
                }
                if (classes.length > 0) {
                    out += '<span class="' + classes.join(' ') + '">';
                    open = true;
                }
            } else {
                var ch = str[i];
                if (ch === '<') out += '&lt;';
                else if (ch === '>') out += '&gt;';
                else if (ch === '&') out += '&amp;';
                else out += ch;
                i++;
            }
        }
        if (open) out += '</span>';
        return out;
    }

    function showToast(type, title, message) {
        var toastContainer = document.getElementById('toast-container');
        if (!toastContainer) return;
        var toast = document.createElement('div');
        toast.className = 'toast ' + type;
        var icon = type === 'success' ? '✓' : type === 'error' ? '✕' : 'ℹ';
        toast.innerHTML = '<span class="toast-icon">' + icon + '</span>' +
            '<div class="toast-content">' +
            '<div class="toast-title">' + escapeHtml(title) + '</div>' +
            '<div class="toast-message">' + escapeHtml(message) + '</div>' +
            '</div>' +
            '<button class="toast-close">✕</button>';
        toastContainer.appendChild(toast);

        setTimeout(function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 4000);

        toast.querySelector('.toast-close').onclick = function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        };
    }

    // Expose to other modules.
    window.GT.escapeHtml = escapeHtml;
    window.GT.ansiToHtml = ansiToHtml;
    window.GT.showToast = showToast;

    // ============================================
    // OUTPUT PANEL (command output overlay)
    // ============================================
    // Shared across modules — any command that returns terminal output uses this.
    function showOutput(cmd, output) {
        var outputPanel = document.getElementById('output-panel');
        var outputContent = document.getElementById('output-panel-content');
        var outputCmd = document.getElementById('output-panel-cmd');
        if (!outputPanel || !outputContent || !outputCmd) return;
        outputCmd.textContent = 'gt ' + cmd;
        outputContent.textContent = output;
        outputPanel.classList.add('open');
    }
    window.GT.showOutput = showOutput;

    // Close button
    var _outputCloseBtn = document.getElementById('output-close-btn');
    if (_outputCloseBtn) {
        _outputCloseBtn.onclick = function() {
            var outputPanel = document.getElementById('output-panel');
            if (outputPanel) outputPanel.classList.remove('open');
        };
    }

    // Copy button
    var _outputCopyBtn = document.getElementById('output-copy-btn');
    if (_outputCopyBtn) {
        _outputCopyBtn.onclick = function() {
            var outputContent = document.getElementById('output-panel-content');
            if (!outputContent) return;
            navigator.clipboard.writeText(outputContent.textContent).then(function() {
                showToast('success', 'Copied', 'Output copied to clipboard');
            });
        };
    }

    // ============================================
    // CSRF PROTECTION
    // ============================================
    // Inject dashboard token into all POST requests to prevent cross-site request forgery.
    var _origFetch = window.fetch;
    var _csrfMeta = document.querySelector('meta[name="dashboard-token"]');
    var _csrfToken = _csrfMeta ? _csrfMeta.getAttribute('content') : '';
    window.fetch = function(url, opts) {
        opts = opts || {};
        if (opts.method && opts.method.toUpperCase() === 'POST' && _csrfToken) {
            opts.headers = opts.headers || {};
            opts.headers['X-Dashboard-Token'] = _csrfToken;
        }
        return _origFetch.call(this, url, opts);
    };

    // ============================================
    // SSE (Server-Sent Events) CONNECTION
    // ============================================
    window.sseConnected = false;
    var evtSource = null;
    var sseReconnectDelay = 1000;
    var sseMaxReconnectDelay = 30000;

    function connectSSE() {
        if (evtSource) {
            evtSource.close();
        }

        evtSource = new EventSource('/api/events');

        evtSource.addEventListener('connected', function() {
            window.sseConnected = true;
            sseReconnectDelay = 1000;
            updateConnectionStatus('live');
        });

        evtSource.addEventListener('dashboard-update', function(e) {
            if (window.pauseRefresh) return;
            // Trigger HTMX to re-fetch the dashboard
            var dashboard = document.getElementById('dashboard-main');
            if (dashboard && typeof htmx !== 'undefined') {
                htmx.trigger(dashboard, 'sse:dashboard-update');
            }
        });

        evtSource.onerror = function() {
            window.sseConnected = false;
            updateConnectionStatus('reconnecting');
            evtSource.close();
            // Exponential backoff reconnect
            setTimeout(function() {
                sseReconnectDelay = Math.min(sseReconnectDelay * 2, sseMaxReconnectDelay);
                connectSSE();
            }, sseReconnectDelay);
        };
    }

    function updateConnectionStatus(state) {
        var el = document.getElementById('connection-status');
        if (!el) return;
        switch (state) {
            case 'live':
                el.textContent = 'Live';
                el.className = 'connection-live';
                break;
            case 'reconnecting':
                el.textContent = 'Reconnecting...';
                el.className = 'connection-reconnecting';
                break;
            default:
                el.textContent = 'Connecting...';
                el.className = '';
        }
    }

    // Start SSE connection
    connectSSE();

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
        updateConnectionStatus(window.sseConnected ? 'live' : 'reconnecting');
    });

})();
