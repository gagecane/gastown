// Dashboard module: crew-panel
// Extracted from dashboard.js lines 1068-1237.
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

    function loadCrew() {
        var loading = document.getElementById('crew-loading');
        var table = document.getElementById('crew-table');
        var tbody = document.getElementById('crew-tbody');
        var empty = document.getElementById('crew-empty');
        var count = document.getElementById('crew-count');

        if (!loading || !table || !tbody) return;

        fetch('/api/crew')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.crew && data.crew.length > 0) {
                    table.style.display = 'table';
                    empty.style.display = 'none';
                    tbody.innerHTML = '';

                    // Check for state changes and notify
                    checkCrewNotifications(data.crew);

                    data.crew.forEach(function(member) {
                        var tr = document.createElement('tr');
                        var rowClass = 'crew-' + member.state;
                        tr.className = rowClass;

                        var stateClass = 'crew-state-' + member.state;
                        var stateText = member.state.charAt(0).toUpperCase() + member.state.slice(1);
                        var stateIcon = '';
                        if (member.state === 'spinning') stateIcon = '🔄 ';
                        else if (member.state === 'finished') stateIcon = '✅ ';
                        else if (member.state === 'questions') stateIcon = '❓ ';
                        else if (member.state === 'ready') stateIcon = '⏸️ ';

                        var sessionBadge = '';
                        if (member.session === 'attached') {
                            sessionBadge = '<span class="badge badge-green">Attached</span>';
                        } else if (member.session === 'detached') {
                            sessionBadge = '<span class="badge badge-muted">Detached</span>';
                        } else {
                            sessionBadge = '<span class="badge badge-muted">None</span>';
                        }

                        // Build the attach command based on the crew member's role
                        var attachCmd = 'gt crew at ' + member.name;
                        if (member.name === 'mayor') {
                            attachCmd = 'gt mayor attach';
                        } else if (member.name === 'deacon') {
                            attachCmd = 'gt deacon attach';
                        } else if (member.name === 'witness' || member.name.startsWith('witness-')) {
                            attachCmd = 'gt witness attach';
                        }

                        tr.innerHTML =
                            '<td><span class="crew-name">' + escapeHtml(member.name) + '</span></td>' +
                            '<td><span class="crew-rig">' + escapeHtml(member.rig) + '</span></td>' +
                            '<td><span class="' + stateClass + '">' + stateIcon + stateText + '</span></td>' +
                            '<td><span class="crew-hook">' + (member.hook ? escapeHtml(member.hook) : '—') + '</span></td>' +
                            '<td class="crew-activity">' + (member.last_active || '—') + '</td>' +
                            '<td>' + sessionBadge + '</td>' +
                            '<td><button class="attach-btn" data-cmd="' + escapeHtml(attachCmd) + '" title="Copy attach command">📎 Attach</button></td>';
                        tbody.appendChild(tr);
                    });

                    if (count) count.textContent = data.total;
                } else {
                    table.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load crew';
                console.error('Crew load error:', err);
            });
    }

    // Track previous crew states for notifications
    var previousCrewStates = {};
    var crewNeedsAttention = 0;

    // Load crew on page load
    loadCrew();
    // Expose for refresh after HTMX swaps
    window.refreshCrewPanel = loadCrew;

    // Crew notification system - check for state changes
    function checkCrewNotifications(crewList) {
        var newNeedsAttention = 0;

        crewList.forEach(function(member) {
            var key = member.rig + '/' + member.name;
            var prevState = previousCrewStates[key];
            var newState = member.state;

            // Count crew needing attention
            if (newState === 'finished' || newState === 'questions') {
                newNeedsAttention++;
            }

            // Notify on state transitions to finished/questions
            if (prevState && prevState !== newState) {
                if (newState === 'finished') {
                    showToast('success', 'Crew Finished', member.name + ' finished their work!');
                    playNotificationSound();
                } else if (newState === 'questions') {
                    showToast('info', 'Needs Attention', member.name + ' has questions for you');
                    playNotificationSound();
                }
            }

            // Update stored state
            previousCrewStates[key] = newState;
        });

        // Update badge on crew panel
        crewNeedsAttention = newNeedsAttention;
        updateCrewBadge();
    }

    function updateCrewBadge() {
        var countEl = document.getElementById('crew-count');
        if (!countEl) return;

        // Add attention indicator if crew needs attention
        if (crewNeedsAttention > 0) {
            countEl.classList.add('needs-attention');
            countEl.setAttribute('data-attention', crewNeedsAttention);
        } else {
            countEl.classList.remove('needs-attention');
            countEl.removeAttribute('data-attention');
        }
    }

    function playNotificationSound() {
        // Simple beep using Web Audio API (optional, non-blocking)
        try {
            var ctx = new (window.AudioContext || window.webkitAudioContext)();
            var oscillator = ctx.createOscillator();
            var gain = ctx.createGain();
            oscillator.connect(gain);
            gain.connect(ctx.destination);
            oscillator.frequency.value = 800;
            gain.gain.value = 0.1;
            oscillator.start();
            oscillator.stop(ctx.currentTime + 0.1);
        } catch (e) {
            // Audio not available, ignore
        }
    }

    // Handle attach button clicks - copy command to clipboard
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.attach-btn');
        if (!btn) return;
        
        e.preventDefault();
        var cmd = btn.getAttribute('data-cmd');
        if (!cmd) return;

        navigator.clipboard.writeText(cmd).then(function() {
            showToast('success', 'Copied', cmd);
        }).catch(function() {
            // Fallback for older browsers
            showToast('info', 'Run in terminal', cmd);
        });
    });


})();
