// Dashboard module: core
// Provides: CSRF-hardened fetch, SSE lifecycle, connection status indicator,
// and the shared `window.gtDash` utility namespace used by all other modules.
//
// This file MUST be loaded first. It installs shared utilities (escapeHtml,
// ansiToHtml, showToast, showOutput, updateConnectionStatus) under window.gtDash
// so feature modules can reach them without relying on a single monolithic IIFE.
(function() {
    'use strict';

    // ============================================
    // SHARED NAMESPACE
    // ============================================
    var gtDash = window.gtDash = window.gtDash || {};

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
    // SHARED UTILITIES
    // ============================================
    function escapeHtml(str) {
        if (!str) return '';
        var div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }
    gtDash.escapeHtml = escapeHtml;

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
    gtDash.ansiToHtml = ansiToHtml;

    // Toast notification helper.
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
    gtDash.showToast = showToast;

    // Output panel helper — displays command output in the overlay panel.
    function showOutput(cmd, output) {
        var outputPanel = document.getElementById('output-panel');
        var outputContent = document.getElementById('output-panel-content');
        var outputCmd = document.getElementById('output-panel-cmd');
        if (!outputPanel || !outputContent) return;
        if (outputCmd) outputCmd.textContent = 'gt ' + cmd;
        outputContent.textContent = output;
        outputPanel.classList.add('open');
    }
    gtDash.showOutput = showOutput;

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
    gtDash.updateConnectionStatus = updateConnectionStatus;

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

    // Start SSE connection
    connectSSE();
})();
