// dashboard/mail.js
// Mail panel: inbox, threads, compose, reply.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;

    // ============================================
    // MAIL PANEL INTERACTIONS
    // ============================================
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


})();
