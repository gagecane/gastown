(function() {
    'use strict';

    // Cross-module helpers from core.js
    var escapeHtml = window.GT.escapeHtml;
    var showToast = window.GT.showToast;
    var ansiToHtml = window.GT.ansiToHtml;

    // ============================================
    // ESCALATION ACTIONS
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.esc-btn');
        if (!btn) return;

        e.preventDefault();
        e.stopPropagation();

        var action = btn.getAttribute('data-action');
        var id = btn.getAttribute('data-id');
        if (!action || !id) return;

        if (action === 'reassign') {
            showReassignPicker(btn, id);
            return;
        }

        // Ack or Resolve - run directly
        var cmdName = 'escalate ' + action + ' ' + id;
        btn.disabled = true;
        btn.textContent = action === 'ack' ? 'Acking...' : 'Resolving...';

        runEscalationAction(cmdName, btn, action);
    });

    function runEscalationAction(cmdName, btn, action) {
        showToast('info', 'Running...', 'gt ' + cmdName);

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmdName, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Success', 'gt ' + cmdName);
                // Remove ack button or fade row on resolve
                var row = btn.closest('.escalation-row');
                if (action === 'resolve' && row) {
                    row.style.opacity = '0.4';
                    row.style.pointerEvents = 'none';
                } else if (action === 'ack' && row) {
                    // Replace ack button with ACK badge
                    btn.outerHTML = '<span class="badge badge-cyan">ACK</span>';
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
                btn.disabled = false;
                btn.textContent = action === 'ack' ? '👍 Ack' : '✓ Resolve';
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
            btn.disabled = false;
            btn.textContent = action === 'ack' ? '👍 Ack' : '✓ Resolve';
        });
    }

    function showReassignPicker(btn, escalationId) {
        // Check if picker already open
        var existing = btn.parentNode.querySelector('.reassign-picker');
        if (existing) {
            existing.remove();
            return;
        }

        var picker = document.createElement('div');
        picker.className = 'reassign-picker';
        picker.innerHTML = '<select class="reassign-select"><option value="">Loading...</option></select>' +
            '<button class="esc-btn esc-reassign-confirm">Go</button>' +
            '<button class="esc-btn esc-reassign-cancel">✕</button>';
        btn.parentNode.appendChild(picker);

        var select = picker.querySelector('.reassign-select');

        // Pause refresh while picker is open
        window.pauseRefresh = true;

        // Load agents
        fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                select.innerHTML = '<option value="">Select agent...</option>';
                var agents = data.agents || [];
                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    var running = typeof agent === 'object' ? agent.running : true;
                    var opt = document.createElement('option');
                    opt.value = name;
                    opt.textContent = name + (running ? '' : ' (stopped)');
                    select.appendChild(opt);
                });
            })
            .catch(function() {
                select.innerHTML = '<option value="">Failed to load</option>';
            });

        // Confirm reassign
        picker.querySelector('.esc-reassign-confirm').addEventListener('click', function() {
            var agent = select.value;
            if (!agent) {
                showToast('error', 'Missing', 'Select an agent to reassign to');
                return;
            }
            picker.remove();
            window.pauseRefresh = false;

            var cmdName = 'escalate reassign ' + escalationId + ' ' + agent;
            btn.disabled = true;
            btn.textContent = 'Reassigning...';

            showToast('info', 'Running...', 'gt ' + cmdName);

            fetch('/api/run', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ command: cmdName, confirmed: true })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.success) {
                    showToast('success', 'Reassigned', 'Escalation reassigned to ' + agent);
                    var row = btn.closest('.escalation-row');
                    if (row) {
                        // Update the "From" cell to show new assignee
                        var fromCell = row.querySelectorAll('td')[2];
                        if (fromCell) fromCell.textContent = '→ ' + agent;
                    }
                } else {
                    showToast('error', 'Failed', data.error || 'Unknown error');
                }
                btn.disabled = false;
                btn.textContent = '↻ Reassign';
            })
            .catch(function(err) {
                showToast('error', 'Error', err.message || 'Request failed');
                btn.disabled = false;
                btn.textContent = '↻ Reassign';
            });
        });

        // Cancel
        picker.querySelector('.esc-reassign-cancel').addEventListener('click', function() {
            picker.remove();
            window.pauseRefresh = false;
        });
    }




})();
