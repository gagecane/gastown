// dashboard/hook.js
// Hook management: attach form, detach, clear all hooks.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // HOOK MANAGEMENT
    // ============================================

    function detachHook(btn) {
        var beadId = btn.getAttribute('data-hook-id');
        if (!beadId) return;

        if (!confirm('Detach hook ' + beadId + '?')) return;

        btn.disabled = true;
        btn.textContent = '...';

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'hook detach ' + beadId, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Detached', beadId + ' detached from hook');
                // Refresh the page to update the hooks panel
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Failed to detach hook');
                btn.disabled = false;
                btn.textContent = 'Detach';
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
            btn.disabled = false;
            btn.textContent = 'Detach';
        });
    }
    window.detachHook = detachHook;

    function openHookAttachForm() {
        var form = document.getElementById('hook-attach-form');
        if (form) {
            form.style.display = 'block';
            var input = document.getElementById('hook-attach-bead');
            if (input) {
                input.value = '';
                setTimeout(function() { input.focus(); }, 50);
            }
        }
    }
    window.openHookAttachForm = openHookAttachForm;

    function closeHookAttachForm() {
        var form = document.getElementById('hook-attach-form');
        if (form) {
            form.style.display = 'none';
        }
    }
    window.closeHookAttachForm = closeHookAttachForm;

    function submitHookAttach() {
        var input = document.getElementById('hook-attach-bead');
        var beadId = input ? input.value.trim() : '';

        if (!beadId) {
            showToast('error', 'Missing', 'Bead ID is required');
            return;
        }

        var submitBtn = document.querySelector('.hook-attach-submit');
        if (submitBtn) {
            submitBtn.disabled = true;
            submitBtn.textContent = '...';
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'hook attach ' + beadId, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Attached', beadId + ' attached to hook');
                closeHookAttachForm();
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Failed to attach hook');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            if (submitBtn) {
                submitBtn.disabled = false;
                submitBtn.textContent = 'Attach';
            }
        });
    }
    window.submitHookAttach = submitHookAttach;

    function clearAllHooks() {
        if (!confirm('Clear ALL hooks? This will detach all hooked work.')) return;

        var rows = document.querySelectorAll('.hook-detach-btn');
        if (rows.length === 0) {
            showToast('info', 'Nothing', 'No hooks to clear');
            return;
        }

        var beadIds = [];
        for (var i = 0; i < rows.length; i++) {
            var id = rows[i].getAttribute('data-hook-id');
            if (id) beadIds.push(id);
        }

        var completed = 0;
        var errors = 0;

        beadIds.forEach(function(beadId) {
            fetch('/api/run', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ command: 'hook detach ' + beadId, confirmed: true })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.success) {
                    completed++;
                } else {
                    errors++;
                }
            })
            .catch(function() {
                errors++;
            })
            .finally(function() {
                if (completed + errors === beadIds.length) {
                    if (errors > 0) {
                        showToast('error', 'Partial', completed + ' detached, ' + errors + ' failed');
                    } else {
                        showToast('success', 'Cleared', completed + ' hook(s) cleared');
                    }
                    if (typeof htmx !== 'undefined') {
                        htmx.trigger(document.body, 'htmx:load');
                    }
                }
            });
        });
    }
    window.clearAllHooks = clearAllHooks;

    // Handle Enter key in hook attach input
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && e.target.id === 'hook-attach-bead') {
            e.preventDefault();
            submitHookAttach();
        }
        if (e.key === 'Escape' && e.target.id === 'hook-attach-bead') {
            e.preventDefault();
            closeHookAttachForm();
        }
    });


})();
