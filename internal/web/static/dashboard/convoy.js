// dashboard/convoy.js
// Convoy panel: list, detail view, create form, interactions.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;
    var showOutput = window.GT.showOutput;

    // ============================================
    // CONVOY PANEL INTERACTIONS
    // ============================================
    var convoyList = document.getElementById('convoy-list');
    var convoyDetail = document.getElementById('convoy-detail');
    var convoyCreateForm = document.getElementById('convoy-create-form');
    var currentConvoyId = null;

    // Click on convoy row to view details
    document.addEventListener('click', function(e) {
        var convoyRow = e.target.closest('.convoy-row');
        if (convoyRow && convoyRow.hasAttribute('data-convoy-id')) {
            e.preventDefault();
            var convoyId = convoyRow.getAttribute('data-convoy-id');
            if (convoyId) {
                openConvoyDetail(convoyId);
            }
        }
    });

    function openConvoyDetail(convoyId) {
        currentConvoyId = convoyId;
        window.pauseRefresh = true;

        // Reset views
        document.getElementById('convoy-detail-id').textContent = convoyId;
        document.getElementById('convoy-detail-title').textContent = 'Convoy: ' + convoyId;
        document.getElementById('convoy-detail-status').textContent = '';
        document.getElementById('convoy-detail-progress').textContent = '';
        document.getElementById('convoy-issues-loading').style.display = 'block';
        document.getElementById('convoy-issues-table').style.display = 'none';
        document.getElementById('convoy-issues-empty').style.display = 'none';
        document.getElementById('convoy-add-issue-form').style.display = 'none';

        // Show detail, hide list and create form
        convoyList.style.display = 'none';
        convoyCreateForm.style.display = 'none';
        convoyDetail.style.display = 'block';

        // Fetch convoy status via /api/run
        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'convoy status ' + convoyId })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            document.getElementById('convoy-issues-loading').style.display = 'none';

            if (!data.success) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                document.getElementById('convoy-issues-empty').querySelector('p').textContent = data.error || 'Failed to load convoy';
                return;
            }

            var issues = parseConvoyStatusOutput(data.output || '');
            if (issues.length === 0) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                return;
            }

            var tbody = document.getElementById('convoy-issues-tbody');
            tbody.innerHTML = '';
            issues.forEach(function(issue) {
                var tr = document.createElement('tr');
                var statusBadge = '';
                var statusLower = (issue.status || '').toLowerCase();
                if (statusLower === 'closed' || statusLower === 'complete' || statusLower === 'done') {
                    statusBadge = '<span class="badge badge-green">Done</span>';
                } else if (statusLower === 'in_progress' || statusLower === 'in progress' || statusLower === 'working') {
                    statusBadge = '<span class="badge badge-yellow">In Progress</span>';
                } else if (statusLower === 'open' || statusLower === 'ready') {
                    statusBadge = '<span class="badge badge-blue">Open</span>';
                } else if (statusLower === 'blocked') {
                    statusBadge = '<span class="badge badge-red">Blocked</span>';
                } else {
                    statusBadge = '<span class="badge badge-muted">' + escapeHtml(issue.status || 'Unknown') + '</span>';
                }

                tr.innerHTML =
                    '<td class="convoy-issue-status">' + statusBadge + '</td>' +
                    '<td><span class="issue-id">' + escapeHtml(issue.id) + '</span></td>' +
                    '<td class="issue-title">' + escapeHtml(issue.title || '') + '</td>' +
                    '<td>' + (issue.assignee ? '<span class="badge badge-blue">' + escapeHtml(issue.assignee) + '</span>' : '<span class="badge badge-muted">Unassigned</span>') + '</td>' +
                    '<td>' + escapeHtml(issue.progress || '') + '</td>';
                tbody.appendChild(tr);
            });
            document.getElementById('convoy-issues-table').style.display = 'table';
        })
        .catch(function(err) {
            document.getElementById('convoy-issues-loading').style.display = 'none';
            document.getElementById('convoy-issues-empty').style.display = 'block';
            document.getElementById('convoy-issues-empty').querySelector('p').textContent = 'Error: ' + err.message;
        });
    }

    // Parse convoy status text output into issue objects
    function parseConvoyStatusOutput(output) {
        var issues = [];
        var lines = output.split('\n');
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i].trim();
            if (!line) continue;
            // Skip header lines and convoy summary lines
            if (line.startsWith('Convoy') || line.startsWith('===') || line.startsWith('---') ||
                line.startsWith('Status:') || line.startsWith('Progress:') || line.startsWith('Created:') ||
                line.startsWith('Title:') || line.startsWith('Issues:') || line.startsWith('Name:')) {
                // Extract convoy-level status/progress for the detail header
                if (line.startsWith('Status:')) {
                    var statusEl = document.getElementById('convoy-detail-status');
                    var statusVal = line.replace('Status:', '').trim().toLowerCase();
                    statusEl.textContent = statusVal;
                    statusEl.className = 'badge';
                    if (statusVal === 'active') statusEl.classList.add('badge-green');
                    else if (statusVal === 'stale') statusEl.classList.add('badge-yellow');
                    else if (statusVal === 'stuck') statusEl.classList.add('badge-red');
                    else if (statusVal === 'complete') statusEl.classList.add('badge-green');
                    else statusEl.classList.add('badge-muted');
                }
                if (line.startsWith('Progress:')) {
                    document.getElementById('convoy-detail-progress').textContent = line.replace('Progress:', '').trim();
                }
                continue;
            }
            // Look for issue lines - typically formatted as:
            // "○ id · title [● P2 · STATUS]" or similar bead-style output
            // Or tabular: "id   title   status   assignee"
            var issue = parseConvoyIssueLine(line);
            if (issue) {
                issues.push(issue);
            }
        }
        return issues;
    }

    // Parse a single issue line from convoy status output
    function parseConvoyIssueLine(line) {
        // Try bead-style format: "○ id · title   [● P2 · OPEN]"
        // or "◐ id · title   [● P2 · IN_PROGRESS]"
        var beadMatch = line.match(/^[○◐●✓]\s+(\S+)\s+[·:]\s+(.+?)(?:\s+\[.*?([A-Z_]+)\])?$/);
        if (beadMatch) {
            var statusFromBracket = '';
            if (beadMatch[3]) {
                statusFromBracket = beadMatch[3].toLowerCase().replace('_', ' ');
            } else {
                // Infer from icon
                if (line.startsWith('✓')) statusFromBracket = 'closed';
                else if (line.startsWith('◐')) statusFromBracket = 'in progress';
                else statusFromBracket = 'open';
            }
            return {
                id: beadMatch[1],
                title: beadMatch[2].trim(),
                status: statusFromBracket,
                assignee: '',
                progress: ''
            };
        }

        // Try simple "id title" format (at least an ID-like token)
        var parts = line.split(/\s{2,}/);
        if (parts.length >= 2 && parts[0].match(/^[a-zA-Z0-9_-]+$/)) {
            return {
                id: parts[0],
                title: parts[1] || '',
                status: parts[2] || '',
                assignee: parts[3] || '',
                progress: parts[4] || ''
            };
        }

        return null;
    }

    // Back button from convoy detail
    document.getElementById('convoy-back-btn').addEventListener('click', function() {
        convoyDetail.style.display = 'none';
        convoyList.style.display = 'block';
        currentConvoyId = null;
        window.pauseRefresh = false;
    });

    // New Convoy button
    document.getElementById('new-convoy-btn').addEventListener('click', function() {
        window.pauseRefresh = true;
        convoyList.style.display = 'none';
        convoyDetail.style.display = 'none';
        convoyCreateForm.style.display = 'block';
        document.getElementById('convoy-create-name').value = '';
        document.getElementById('convoy-create-issues').value = '';
        document.getElementById('convoy-create-name').focus();
    });

    // Cancel create convoy
    document.getElementById('convoy-create-back-btn').addEventListener('click', cancelConvoyCreate);
    document.getElementById('convoy-create-cancel-btn').addEventListener('click', cancelConvoyCreate);

    function cancelConvoyCreate() {
        convoyCreateForm.style.display = 'none';
        convoyList.style.display = 'block';
        window.pauseRefresh = false;
    }

    // Submit create convoy
    document.getElementById('convoy-create-submit-btn').addEventListener('click', function() {
        var name = document.getElementById('convoy-create-name').value.trim();
        var issuesStr = document.getElementById('convoy-create-issues').value.trim();

        if (!name) {
            showToast('error', 'Missing', 'Convoy name is required');
            return;
        }

        var btn = document.getElementById('convoy-create-submit-btn');
        btn.disabled = true;
        btn.textContent = 'Creating...';

        // Build command: convoy create <name> [issue1 issue2 ...]
        var cmd = 'convoy create ' + name;
        if (issuesStr) {
            cmd += ' ' + issuesStr;
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Created', 'Convoy "' + name + '" created');
                cancelConvoyCreate();
                if (data.output && data.output.trim()) {
                    showOutput(cmd, data.output);
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Create Convoy';
        });
    });

    // Add Issue button in convoy detail
    document.getElementById('convoy-add-issue-btn').addEventListener('click', function() {
        var form = document.getElementById('convoy-add-issue-form');
        form.style.display = form.style.display === 'none' ? 'flex' : 'none';
        if (form.style.display !== 'none') {
            document.getElementById('convoy-add-issue-input').value = '';
            document.getElementById('convoy-add-issue-input').focus();
        }
    });

    // Cancel add issue
    document.getElementById('convoy-add-issue-cancel').addEventListener('click', function() {
        document.getElementById('convoy-add-issue-form').style.display = 'none';
    });

    // Submit add issue to convoy
    document.getElementById('convoy-add-issue-submit').addEventListener('click', submitAddIssueToConvoy);

    // Enter key in add issue input
    document.getElementById('convoy-add-issue-input').addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            submitAddIssueToConvoy();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            document.getElementById('convoy-add-issue-form').style.display = 'none';
        }
    });

    function submitAddIssueToConvoy() {
        var issueId = document.getElementById('convoy-add-issue-input').value.trim();
        if (!issueId || !currentConvoyId) {
            showToast('error', 'Missing', 'Issue ID is required');
            return;
        }

        var btn = document.getElementById('convoy-add-issue-submit');
        btn.disabled = true;
        btn.textContent = 'Adding...';

        var cmd = 'convoy add ' + currentConvoyId + ' ' + issueId;

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Added', 'Issue ' + issueId + ' added to convoy');
                document.getElementById('convoy-add-issue-form').style.display = 'none';
                // Refresh the convoy detail view
                openConvoyDetail(currentConvoyId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Add';
        });
    }

    // Click on mail thread header - toggle expand or open single message
    document.addEventListener('click', function(e) {
        // Handle click on individual message within expanded thread
        var threadMsg = e.target.closest('.mail-thread-msg');
        if (threadMsg) {
            e.preventDefault();
            var msgId = threadMsg.getAttribute('data-msg-id');
            var from = threadMsg.getAttribute('data-from');
            if (msgId) {
                openMailDetail(msgId, from);
            }
            return;
        }

        // Handle click on thread header
        var threadHeader = e.target.closest('.mail-thread-header');
        if (threadHeader) {
            e.preventDefault();
            var msgId = threadHeader.getAttribute('data-msg-id');
            if (msgId) {
                // Single message thread - open directly
                var from = threadHeader.getAttribute('data-from');
                openMailDetail(msgId, from);
            } else {
                // Multi-message thread - toggle expand/collapse
                var threadEl = threadHeader.closest('.mail-thread');
                var msgsEl = threadEl ? threadEl.querySelector('.mail-thread-messages') : null;
                if (msgsEl) {
                    var isExpanded = msgsEl.style.display !== 'none';
                    msgsEl.style.display = isExpanded ? 'none' : 'block';
                    threadEl.classList.toggle('mail-thread-expanded', !isExpanded);
                }
            }
            return;
        }

        // Legacy: handle click on mail-row (All Traffic tab)
        var mailRow = e.target.closest('.mail-row');
        if (mailRow) {
            e.preventDefault();
            var msgId = mailRow.getAttribute('data-msg-id');
            var from = mailRow.getAttribute('data-from');
            if (msgId) {
                openMailDetail(msgId, from);
            }
        }
    });

    function openMailDetail(msgId, from) {
        currentMessageId = msgId;
        currentMessageFrom = from;

        // Pause HTMX refresh while viewing/composing mail
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('mail-detail-subject').textContent = 'Loading...';
        document.getElementById('mail-detail-from').textContent = from || '';
        document.getElementById('mail-detail-body').textContent = '';
        document.getElementById('mail-detail-time').textContent = '';

        // Hide both list views and compose, show detail
        mailList.style.display = 'none';
        if (mailAll) mailAll.style.display = 'none';
        mailCompose.style.display = 'none';
        mailDetail.style.display = 'block';

        // Fetch message content
        fetch('/api/mail/read?id=' + encodeURIComponent(msgId))
            .then(function(r) { return r.json(); })
            .then(function(msg) {
                document.getElementById('mail-detail-subject').textContent = msg.subject || '(no subject)';
                document.getElementById('mail-detail-from').textContent = msg.from || from;
                document.getElementById('mail-detail-body').textContent = msg.body || '(no content)';
                document.getElementById('mail-detail-time').textContent = msg.timestamp || '';
            })
            .catch(function(err) {
                document.getElementById('mail-detail-body').textContent = 'Error loading message: ' + err.message;
            });
    }

    // Back button from detail view - return to correct tab
    document.getElementById('mail-back-btn').addEventListener('click', function() {
        mailDetail.style.display = 'none';
        mailCompose.style.display = 'none';

        // Return to the correct view based on current tab
        if (currentMailTab === 'all' && mailAll) {
            mailAll.style.display = 'block';
            mailList.style.display = 'none';
        } else {
            mailList.style.display = 'block';
            if (mailAll) mailAll.style.display = 'none';
        }

        currentMessageId = null;
        currentMessageFrom = null;
        // Resume HTMX refresh
        window.pauseRefresh = false;
    });

    // Reply button
    document.getElementById('mail-reply-btn').addEventListener('click', function() {
        var subject = document.getElementById('mail-detail-subject').textContent;
        var replySubject = subject.startsWith('Re: ') ? subject : 'Re: ' + subject;

        document.getElementById('mail-compose-title').textContent = 'Reply';
        document.getElementById('compose-subject').value = replySubject;
        document.getElementById('compose-reply-to').value = currentMessageId || '';
        document.getElementById('compose-body').value = '';

        // Populate To dropdown and select the sender
        populateToDropdown(currentMessageFrom);

        mailDetail.style.display = 'none';
        mailCompose.style.display = 'block';
        document.getElementById('compose-body').focus();
    });

    // Compose new message button
    document.getElementById('compose-mail-btn').addEventListener('click', function() {
        // Pause HTMX refresh while composing
        window.pauseRefresh = true;

        document.getElementById('mail-compose-title').textContent = 'New Message';
        document.getElementById('compose-subject').value = '';
        document.getElementById('compose-body').value = '';
        document.getElementById('compose-reply-to').value = '';

        // Populate To dropdown
        populateToDropdown(null);

        // Hide all mail views, show compose
        mailList.style.display = 'none';
        if (mailAll) mailAll.style.display = 'none';
        mailDetail.style.display = 'none';
        mailCompose.style.display = 'block';
        document.getElementById('compose-to').focus();
    });

    // Back button from compose view
    document.getElementById('compose-back-btn').addEventListener('click', function() {
        mailCompose.style.display = 'none';
        if (currentMessageId) {
            mailDetail.style.display = 'block';
        } else if (currentMailTab === 'all' && mailAll) {
            mailAll.style.display = 'block';
        } else {
            mailList.style.display = 'block';
        }
    });

    // Cancel compose
    document.getElementById('compose-cancel-btn').addEventListener('click', function() {
        mailCompose.style.display = 'none';
        mailList.style.display = 'block';
        currentMessageId = null;
        currentMessageFrom = null;
        // Resume HTMX refresh
        window.pauseRefresh = false;
    });

    // Send message
    document.getElementById('mail-send-btn').addEventListener('click', function() {
        var to = document.getElementById('compose-to').value;
        var subject = document.getElementById('compose-subject').value;
        var body = document.getElementById('compose-body').value;
        var replyTo = document.getElementById('compose-reply-to').value;

        if (!to || !subject) {
            showToast('error', 'Missing fields', 'Please fill in To and Subject');
            return;
        }

        var btn = document.getElementById('mail-send-btn');
        btn.textContent = 'Sending...';
        btn.disabled = true;

        fetch('/api/mail/send', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                to: to,
                subject: subject,
                body: body,
                reply_to: replyTo || undefined
            })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Sent', 'Message sent to ' + to);
                mailCompose.style.display = 'none';
                mailList.style.display = 'block';
                currentMessageId = null;
                currentMessageFrom = null;
                // Resume HTMX refresh and reload inbox
                window.pauseRefresh = false;
                loadMailInbox();
            } else {
                showToast('error', 'Failed', data.error || 'Failed to send message');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.textContent = 'Send';
            btn.disabled = false;
        });
    });

    // Populate To dropdown with agents
    // Returns a Promise so callers can wait for it
    function populateToDropdown(selectedValue) {
        var select = document.getElementById('compose-to');
        
        // Show loading state
        select.innerHTML = '<option value="">⏳ Loading recipients...</option>';
        select.disabled = true;

        // If we have a selected value for reply, add it immediately so it's available
        if (selectedValue) {
            var cleanValue = selectedValue.replace(/\/$/, '').trim();
            var opt = document.createElement('option');
            opt.value = cleanValue;
            opt.textContent = cleanValue + ' (replying to)';
            opt.selected = true;
            select.appendChild(opt);
            select.disabled = false;
        }

        // Fetch agents from options API
        return fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                // Clear loading state, rebuild options
                select.innerHTML = '<option value="">Select recipient...</option>';
                
                // Re-add reply-to if present
                if (selectedValue) {
                    var cleanVal = selectedValue.replace(/\/$/, '').trim();
                    var replyOpt = document.createElement('option');
                    replyOpt.value = cleanVal;
                    replyOpt.textContent = cleanVal + ' (replying to)';
                    replyOpt.selected = true;
                    select.appendChild(replyOpt);
                }
                
                var agents = data.agents || [];
                var addedValues = selectedValue ? [selectedValue.replace(/\/$/, '').toLowerCase()] : [];

                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    var running = typeof agent === 'object' ? agent.running : true;

                    // Skip if already added as reply-to
                    if (addedValues.indexOf(name.toLowerCase()) !== -1) {
                        return;
                    }

                    var opt = document.createElement('option');
                    opt.value = name;
                    opt.textContent = name + (running ? ' (● running)' : ' (○ stopped)');
                    if (!running) opt.disabled = true;
                    select.appendChild(opt);
                });
                
                select.disabled = false;
            })
            .catch(function(err) {
                console.error('Failed to load agents for To dropdown:', err);
                select.innerHTML = '<option value="">⚠ Failed to load recipients</option>';
                select.disabled = false;
            });
    }


})();
