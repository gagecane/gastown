// Dashboard module: mail-panel
// Extracted from dashboard.js lines 896-1064.
// Depends on: window.gtDash (escapeHtml, ansiToHtml, showToast, showOutput,
// updateConnectionStatus — aliased below).
(function() {
    'use strict';

    var gtDash = window.gtDash || {};
    var escapeHtml = gtDash.escapeHtml;
    var ansiToHtml = gtDash.ansiToHtml;
    var showToast = gtDash.showToast;
    var showOutput = gtDash.showOutput;
    var updateConnectionStatus = gtDash.updateConnectionStatus;

    var mailList = document.getElementById('mail-list');
    var mailAll = document.getElementById('mail-all');
    var mailDetail = document.getElementById('mail-detail');
    var mailCompose = document.getElementById('mail-compose');
    var currentMessageId = null;
    var currentMessageFrom = null;
    var currentMailTab = 'inbox';

    // Mail tab switching
    document.querySelectorAll('.mail-tab').forEach(function(tab) {
        tab.addEventListener('click', function() {
            var targetTab = tab.getAttribute('data-tab');
            if (targetTab === currentMailTab) return;

            // Update active tab
            document.querySelectorAll('.mail-tab').forEach(function(t) {
                t.classList.remove('active');
            });
            tab.classList.add('active');
            currentMailTab = targetTab;

            // Show/hide views
            if (targetTab === 'inbox') {
                mailList.style.display = 'block';
                mailAll.style.display = 'none';
            } else {
                mailList.style.display = 'none';
                mailAll.style.display = 'block';
            }

            // Hide detail/compose views
            mailDetail.style.display = 'none';
            mailCompose.style.display = 'none';
        });
    });

    // Load mail inbox as threaded conversations
    function loadMailInbox() {
        var loading = document.getElementById('mail-loading');
        var threadsContainer = document.getElementById('mail-threads');
        var empty = document.getElementById('mail-empty');
        var count = document.getElementById('mail-count');

        if (!loading || !threadsContainer) return;

        fetch('/api/mail/threads')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.threads && data.threads.length > 0) {
                    threadsContainer.style.display = 'block';
                    empty.style.display = 'none';
                    threadsContainer.innerHTML = '';

                    data.threads.forEach(function(thread) {
                        var threadEl = document.createElement('div');
                        threadEl.className = 'mail-thread' + (thread.unread_count > 0 ? ' mail-thread-unread' : '');

                        var last = thread.last_message;
                        var hasMultiple = thread.count > 1;
                        var countBadge = hasMultiple ? '<span class="thread-count">' + thread.count + '</span>' : '';
                        var unreadDot = thread.unread_count > 0 ? '<span class="thread-unread-dot"></span>' : '';

                        var priorityIcon = '';
                        if (last.priority === 'urgent') priorityIcon = '<span class="priority-urgent">⚡</span> ';
                        else if (last.priority === 'high') priorityIcon = '<span class="priority-high">!</span> ';

                        // Thread header (always visible)
                        var headerEl = document.createElement('div');
                        headerEl.className = 'mail-thread-header';
                        headerEl.setAttribute('data-thread-id', thread.thread_id);
                        headerEl.innerHTML =
                            '<div class="mail-thread-left">' +
                                unreadDot +
                                '<span class="mail-from">' + escapeHtml(last.from) + '</span>' +
                                countBadge +
                            '</div>' +
                            '<div class="mail-thread-center">' +
                                priorityIcon +
                                '<span class="mail-subject">' + escapeHtml(thread.subject) + '</span>' +
                                (hasMultiple ? '<span class="mail-thread-preview"> — ' + escapeHtml(last.body ? last.body.substring(0, 60) : '') + '</span>' : '') +
                            '</div>' +
                            '<div class="mail-thread-right">' +
                                '<span class="mail-time">' + formatMailTime(last.timestamp) + '</span>' +
                            '</div>';

                        threadEl.appendChild(headerEl);

                        // Thread messages (collapsed by default, only for multi-message threads)
                        if (hasMultiple) {
                            var msgsEl = document.createElement('div');
                            msgsEl.className = 'mail-thread-messages';
                            msgsEl.style.display = 'none';

                            thread.messages.forEach(function(msg) {
                                var msgEl = document.createElement('div');
                                msgEl.className = 'mail-thread-msg' + (msg.read ? '' : ' mail-unread');
                                msgEl.setAttribute('data-msg-id', msg.id);
                                msgEl.setAttribute('data-from', msg.from);
                                msgEl.innerHTML =
                                    '<div class="mail-thread-msg-header">' +
                                        '<span class="mail-from">' + escapeHtml(msg.from) + '</span>' +
                                        '<span class="mail-time">' + formatMailTime(msg.timestamp) + '</span>' +
                                    '</div>' +
                                    '<div class="mail-thread-msg-subject">' + escapeHtml(msg.subject) + '</div>';
                                msgsEl.appendChild(msgEl);
                            });

                            threadEl.appendChild(msgsEl);
                        } else {
                            // Single message thread - clicking opens the message directly
                            headerEl.setAttribute('data-msg-id', last.id);
                            headerEl.setAttribute('data-from', last.from);
                        }

                        threadsContainer.appendChild(threadEl);
                    });

                    // Update count
                    if (count) {
                        var unread = data.unread_count || 0;
                        count.textContent = unread > 0 ? unread + ' unread' : data.total;
                        if (unread > 0) count.classList.add('has-unread');
                        else count.classList.remove('has-unread');
                    }
                } else {
                    threadsContainer.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load mail';
                console.error('Mail load error:', err);
            });
    }

    function formatMailTime(timestamp) {
        if (!timestamp) return '';
        var d = new Date(timestamp);
        var now = new Date();
        var diff = now - d;

        // Format: "Jan 26, 3:45 PM" or "Jan 26 2025, 3:45 PM" if different year
        var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        var month = months[d.getMonth()];
        var day = d.getDate();
        var hours = d.getHours();
        var minutes = d.getMinutes();
        var ampm = hours >= 12 ? 'PM' : 'AM';
        hours = hours % 12 || 12;
        var minStr = minutes < 10 ? '0' + minutes : minutes;
        var yearPart = d.getFullYear() !== now.getFullYear() ? ' ' + d.getFullYear() + ',' : '';
        var dateStr = month + ' ' + day + yearPart + ', ' + hours + ':' + minStr + ' ' + ampm;

        // Add relative time in parentheses for recent messages
        var relative = '';
        if (diff < 60000) relative = ' (just now)';
        else if (diff < 3600000) relative = ' (' + Math.floor(diff / 60000) + 'm ago)';
        else if (diff < 86400000) relative = ' (' + Math.floor(diff / 3600000) + 'h ago)';
        else if (diff < 604800000) relative = ' (' + Math.floor(diff / 86400000) + 'd ago)';

        return dateStr + relative;
    }

    // Load mail on page load
    loadMailInbox();

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
