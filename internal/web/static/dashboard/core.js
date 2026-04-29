// dashboard/core.js
// Shared helpers + CSRF + SSE + connection-status + expand/collapse handlers.
// Exposes cross-module helpers on window.GT.
// Must be loaded BEFORE every other dashboard/*.js file.
(function() {
    'use strict';

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
    // SHARED HELPERS (exposed on window.GT)
    // ============================================
    var GT = window.GT = window.GT || {};

    GT.escapeHtml = function(str) {
        if (!str) return '';
        var div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    };

    // Convert ANSI escape sequences to HTML spans with CSS classes.
    GT.ansiToHtml = function(str) {
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
    };

    // Output panel (shared: used by palette, convoy, slings to display command output).
    // Setup is deferred to DOMContentLoaded because core.js may load before the DOM
    // nodes exist (e.g. in the <head>). Before DOM ready, showOutput is a no-op.
    GT.showOutput = function(cmd, output) {
        var outputCmd = document.getElementById('output-panel-cmd');
        var outputContent = document.getElementById('output-panel-content');
        var outputPanel = document.getElementById('output-panel');
        if (!outputCmd || !outputContent || !outputPanel) return;
        outputCmd.textContent = 'gt ' + cmd;
        outputContent.textContent = output;
        outputPanel.classList.add('open');
    };

    function wireOutputPanelButtons() {
        var outputPanel = document.getElementById('output-panel');
        var outputContent = document.getElementById('output-panel-content');
        var closeBtn = document.getElementById('output-close-btn');
        var copyBtn = document.getElementById('output-copy-btn');
        if (closeBtn && outputPanel) {
            closeBtn.onclick = function() {
                outputPanel.classList.remove('open');
            };
        }
        if (copyBtn && outputContent) {
            copyBtn.onclick = function() {
                navigator.clipboard.writeText(outputContent.textContent).then(function() {
                    GT.showToast('success', 'Copied', 'Output copied to clipboard');
                });
            };
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', wireOutputPanelButtons);
    } else {
        wireOutputPanelButtons();
    }

    GT.showToast = function(type, title, message) {
        var toastContainer = document.getElementById('toast-container');
        if (!toastContainer) return;
        var toast = document.createElement('div');
        toast.className = 'toast ' + type;
        var icon = type === 'success' ? '✓' : type === 'error' ? '✕' : 'ℹ';
        toast.innerHTML = '<span class="toast-icon">' + icon + '</span>' +
            '<div class="toast-content">' +
            '<div class="toast-title">' + GT.escapeHtml(title) + '</div>' +
            '<div class="toast-message">' + GT.escapeHtml(message) + '</div>' +
            '</div>' +
            '<button class="toast-close">✕</button>';
        toastContainer.appendChild(toast);

        setTimeout(function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 4000);

        toast.querySelector('.toast-close').onclick = function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        };
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

    GT.updateConnectionStatus = updateConnectionStatus;

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
