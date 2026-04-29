// dashboard/palette.js
// Command palette (Cmd+K) — fuzzy command search, arg forms, execution via /api/run.
// Depends on window.GT (escapeHtml, ansiToHtml, showToast, showOutput) from core.js.
(function() {
    'use strict';

    var escapeHtml = window.GT.escapeHtml;
    var ansiToHtml = window.GT.ansiToHtml;
    var showToast = window.GT.showToast;
    var showOutput = window.GT.showOutput;

    var allCommands = [];
    var visibleCommands = [];
    var selectedIdx = 0;
    var isPaletteOpen = false;
    var executionLock = false;
    var pendingCommand = null; // Command waiting for args
    var cachedOptions = null;  // Cached options from /api/options
    var recentCommands = [];   // Recently executed commands (from localStorage)
    var MAX_RECENT = 10;
    var RECENT_STORAGE_KEY = 'gt-palette-recent';

    // Load recent commands from localStorage
    function loadRecentCommands() {
        try {
            var stored = localStorage.getItem(RECENT_STORAGE_KEY);
            if (stored) {
                recentCommands = JSON.parse(stored);
                if (!Array.isArray(recentCommands)) recentCommands = [];
                // Cap at MAX_RECENT
                recentCommands = recentCommands.slice(0, MAX_RECENT);
            }
        } catch (e) {
            recentCommands = [];
        }
    }

    // Save a command to recent history
    function saveRecentCommand(cmdName) {
        // Remove duplicate if exists
        recentCommands = recentCommands.filter(function(c) { return c !== cmdName; });
        // Add to front
        recentCommands.unshift(cmdName);
        // Cap at MAX_RECENT
        recentCommands = recentCommands.slice(0, MAX_RECENT);
        try {
            localStorage.setItem(RECENT_STORAGE_KEY, JSON.stringify(recentCommands));
        } catch (e) {
            // localStorage full or unavailable, ignore
        }
    }

    // Detect active context based on expanded panel or visible detail view
    function detectActiveContext() {
        var expandedPanel = document.querySelector('.panel.expanded');
        if (expandedPanel) {
            var panelId = expandedPanel.id || '';
            if (panelId.indexOf('mail') !== -1) return 'Mail';
            if (panelId.indexOf('crew') !== -1) return 'Crew';
            if (panelId.indexOf('issue') !== -1 || panelId.indexOf('work') !== -1) return 'Work';
            if (panelId.indexOf('ready') !== -1) return 'Work';
            if (panelId.indexOf('pr') !== -1 || panelId.indexOf('merge') !== -1) return 'Status';
        }
        // Check detail views
        var mailDetail = document.getElementById('mail-detail');
        var mailCompose = document.getElementById('mail-compose');
        if ((mailDetail && mailDetail.style.display !== 'none') ||
            (mailCompose && mailCompose.style.display !== 'none')) return 'Mail';
        var issueDetail = document.getElementById('issue-detail');
        if (issueDetail && issueDetail.style.display !== 'none') return 'Work';
        var prDetail = document.getElementById('pr-detail');
        if (prDetail && prDetail.style.display !== 'none') return 'Status';
        return null;
    }

    // Score a command for fuzzy matching. Returns -1 for no match, higher is better.
    function scoreCommand(cmd, query) {
        var name = cmd.name.toLowerCase();
        var desc = cmd.desc.toLowerCase();
        var cat = cmd.category.toLowerCase();
        var q = query.toLowerCase();

        // Exact prefix match on name is best
        if (name.indexOf(q) === 0) return 100 + (50 - name.length);
        // Prefix match on a word within the name
        var nameParts = name.split(' ');
        for (var i = 0; i < nameParts.length; i++) {
            if (nameParts[i].indexOf(q) === 0) return 80 + (50 - name.length);
        }
        // Substring match in name
        if (name.indexOf(q) !== -1) return 60 + (50 - name.length);
        // Match in description
        if (desc.indexOf(q) !== -1) return 40;
        // Match in category
        if (cat.indexOf(q) !== -1) return 20;
        // Fuzzy: all query chars appear in order in name
        var ni = 0;
        for (var qi = 0; qi < q.length; qi++) {
            ni = name.indexOf(q[qi], ni);
            if (ni === -1) return -1;
            ni++;
        }
        return 10;
    }

    // Highlight matching portions in text for display
    function highlightMatch(text, query) {
        if (!query) return escapeHtml(text);
        var lowerText = text.toLowerCase();
        var lowerQuery = query.toLowerCase();
        var idx = lowerText.indexOf(lowerQuery);
        if (idx !== -1) {
            return escapeHtml(text.substring(0, idx)) +
                '<mark>' + escapeHtml(text.substring(idx, idx + query.length)) + '</mark>' +
                escapeHtml(text.substring(idx + query.length));
        }
        return escapeHtml(text);
    }

    loadRecentCommands();

    var overlay = document.getElementById('command-palette-overlay');
    var searchInput = document.getElementById('command-palette-input');
    var resultsDiv = document.getElementById('command-palette-results');
    var toastContainer = document.getElementById('toast-container');
    // Load commands once
    fetch('/api/commands')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            allCommands = data.commands || [];
        })
        .catch(function() {
            console.error('Failed to load commands');
        });

    // Fetch dynamic options (rigs, polecats, convoys, agents, hooks)
    function fetchOptions() {
        return fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                cachedOptions = data;
                return data;
            })
            .catch(function() {
                console.error('Failed to load options');
                return null;
            });
    }

    // Get options for a specific argType
    // Returns array of {value, label, disabled} objects
    function getOptionsForType(argType) {
        if (!cachedOptions) return [];

        var rawOptions;
        switch (argType) {
            case 'rigs': rawOptions = cachedOptions.rigs || []; break;
            case 'polecats': rawOptions = cachedOptions.polecats || []; break;
            case 'convoys': rawOptions = cachedOptions.convoys || []; break;
            case 'agents': rawOptions = cachedOptions.agents || []; break;
            case 'hooks': rawOptions = cachedOptions.hooks || []; break;
            case 'messages': rawOptions = cachedOptions.messages || []; break;
            case 'crew': rawOptions = cachedOptions.crew || []; break;
            case 'escalations': rawOptions = cachedOptions.escalations || []; break;
            default: return [];
        }

        // Normalize to {value, label, disabled} format
        return rawOptions.map(function(opt) {
            if (typeof opt === 'string') {
                return { value: opt, label: opt, disabled: false };
            }
            // Agent format: {name, status, running}
            var statusText = opt.running ? '● running' : '○ stopped';
            return {
                value: opt.name,
                label: opt.name + ' (' + statusText + ')',
                disabled: !opt.running,
                running: opt.running
            };
        });
    }

    // Returns [{name: "address", flag: null}, {name: "subject", flag: "-s"}, {name: "message", flag: "-m"}]
    function parseArgsTemplate(argsStr) {
        if (!argsStr) return [];
        var args = [];
        // Match patterns like "<name>" or "-f <name>"
        var regex = /(?:(-\w+)\s+)?<([^>]+)>/g;
        var match;
        while ((match = regex.exec(argsStr)) !== null) {
            args.push({ name: match[2], flag: match[1] || null });
        }
        return args;
    }

    function renderResults() {
        // If waiting for args, show the args input with options
        if (pendingCommand) {
            var options = pendingCommand.argType ? getOptionsForType(pendingCommand.argType) : [];
            var argFields = parseArgsTemplate(pendingCommand.args);

            var formHtml = '<div class="command-args-prompt">' +
                '<div class="command-args-header">gt ' + escapeHtml(pendingCommand.name) + '</div>';

            // Build form fields for each argument
            for (var i = 0; i < argFields.length; i++) {
                var field = argFields[i];
                var fieldId = 'arg-field-' + i;
                var isFirstField = (i === 0) && !field.flag; // First positional arg
                var hasOptions = isFirstField && pendingCommand.argType && options.length > 0;
                var noOptions = isFirstField && pendingCommand.argType && options.length === 0;
                var isMessageField = field.name === 'message' || field.name === 'body';

                formHtml += '<div class="command-field">';
                formHtml += '<label class="command-field-label" for="' + fieldId + '">' + escapeHtml(field.name) + '</label>';

                if (hasOptions) {
                    // Dropdown for first arg when options exist
                    formHtml += '<select id="' + fieldId + '" class="command-field-select" data-flag="' + (field.flag || '') + '">';
                    formHtml += '<option value="">Select ' + escapeHtml(field.name) + '...</option>';
                    for (var j = 0; j < options.length; j++) {
                        var opt = options[j];
                        var disabledAttr = opt.disabled ? ' disabled' : '';
                        var optClass = opt.disabled ? ' class="option-disabled"' : (opt.running ? ' class="option-running"' : '');
                        formHtml += '<option value="' + escapeHtml(opt.value) + '"' + disabledAttr + optClass + '>' + escapeHtml(opt.label) + '</option>';
                    }
                    formHtml += '</select>';
                } else if (noOptions) {
                    formHtml += '<input type="text" id="' + fieldId + '" class="command-field-input" data-flag="' + (field.flag || '') + '" placeholder="No ' + escapeHtml(pendingCommand.argType) + ' available">';
                } else if (isMessageField) {
                    formHtml += '<textarea id="' + fieldId + '" class="command-field-textarea" data-flag="' + (field.flag || '') + '" placeholder="Enter ' + escapeHtml(field.name) + '..." rows="3"></textarea>';
                } else {
                    formHtml += '<input type="text" id="' + fieldId + '" class="command-field-input" data-flag="' + (field.flag || '') + '" placeholder="Enter ' + escapeHtml(field.name) + '...">';
                }
                formHtml += '</div>';
            }

            // If no arg fields parsed, show generic input
            if (argFields.length === 0 && pendingCommand.args) {
                formHtml += '<div class="command-field">';
                formHtml += '<input type="text" id="arg-field-0" class="command-field-input" placeholder="' + escapeHtml(pendingCommand.args) + '">';
                formHtml += '</div>';
            }

            formHtml += '<div class="command-args-actions">' +
                '<button id="command-args-run" class="command-args-btn run">Run</button>' +
                '<button id="command-args-cancel" class="command-args-btn cancel">Cancel</button>' +
                '</div></div>';

            resultsDiv.innerHTML = formHtml;

            // Focus first field
            var firstField = resultsDiv.querySelector('#arg-field-0');
            if (firstField) firstField.focus();

            // Wire up run/cancel buttons
            var runBtn = document.getElementById('command-args-run');
            var cancelBtn = document.getElementById('command-args-cancel');

            if (runBtn) {
                runBtn.onclick = function() {
                    runBtn.classList.add('loading');
                    runBtn.textContent = 'Running';
                    runWithArgsFromForm(argFields.length || 1);
                };
            }
            if (cancelBtn) {
                cancelBtn.onclick = cancelArgs;
            }

            // Enter key submits
            resultsDiv.querySelectorAll('input, select').forEach(function(el) {
                el.onkeydown = function(e) {
                    if (e.key === 'Enter') {
                        e.preventDefault();
                        runWithArgsFromForm(argFields.length || 1);
                    } else if (e.key === 'Escape') {
                        e.preventDefault();
                        cancelArgs();
                    }
                };
            });
            return;
        }

        if (visibleCommands.length === 0) {
            resultsDiv.innerHTML = '<div class="command-palette-empty">No matching commands</div>';
            return;
        }
        var currentQuery = searchInput ? searchInput.value.trim() : '';
        var html = '';

        if (currentQuery) {
            // Search mode: flat list with highlights
            for (var i = 0; i < visibleCommands.length; i++) {
                var cmd = visibleCommands[i];
                var cls = 'command-item' + (i === selectedIdx ? ' selected' : '');
                var argsHint = cmd.args ? ' <span class="command-args">' + escapeHtml(cmd.args) + '</span>' : '';
                var nameHtml = highlightMatch('gt ' + cmd.name, currentQuery);
                html += '<div class="' + cls + '" data-cmd-name="' + escapeHtml(cmd.name) + '" data-cmd-args="' + escapeHtml(cmd.args || '') + '">' +
                    '<span class="command-name">' + nameHtml + argsHint + '</span>' +
                    '<span class="command-desc">' + escapeHtml(cmd.desc) + '</span>' +
                    '<span class="command-category">' + escapeHtml(cmd.category) + '</span>' +
                    '</div>';
            }
        } else {
            // Browse mode: show Recent, Contextual, then All Commands
            // visibleCommands was rebuilt by filterCommands with sections baked in
            for (var j = 0; j < visibleCommands.length; j++) {
                var item = visibleCommands[j];
                if (item._section) {
                    // Section header
                    html += '<div class="command-section-header">' + escapeHtml(item._section) + '</div>';
                    continue;
                }
                var cls2 = 'command-item' + (j === selectedIdx ? ' selected' : '');
                var argsHint2 = item.args ? ' <span class="command-args">' + escapeHtml(item.args) + '</span>' : '';
                var icon = item._recent ? '<span class="command-recent-icon">&#8635;</span>' : '';
                html += '<div class="' + cls2 + '" data-cmd-name="' + escapeHtml(item.name) + '" data-cmd-args="' + escapeHtml(item.args || '') + '">' +
                    icon +
                    '<span class="command-name">gt ' + escapeHtml(item.name) + argsHint2 + '</span>' +
                    '<span class="command-desc">' + escapeHtml(item.desc) + '</span>' +
                    '<span class="command-category">' + escapeHtml(item.category) + '</span>' +
                    '</div>';
            }
        }
        resultsDiv.innerHTML = html;

        // Scroll selected item into view
        var selectedEl = resultsDiv.querySelector('.command-item.selected');
        if (selectedEl) {
            selectedEl.scrollIntoView({ block: 'nearest' });
        }
    }

    function runWithArgsFromForm(fieldCount) {
        var args = [];
        for (var i = 0; i < fieldCount; i++) {
            var field = document.getElementById('arg-field-' + i);
            if (field) {
                var val = field.value.trim();
                var flag = field.getAttribute('data-flag');
                if (val) {
                    if (flag) {
                        // Flag-based arg: -s "value"
                        args.push(flag);
                        args.push('"' + val.replace(/"/g, '\\"') + '"');
                    } else {
                        // Positional arg
                        args.push(val);
                    }
                }
            }
        }
        if (pendingCommand) {
            var fullCmd = pendingCommand.name + (args.length ? ' ' + args.join(' ') : '');
            pendingCommand = null;
            runCommand(fullCmd);
        }
    }

    function runWithArgs() {
        runWithArgsFromForm(10); // fallback
    }

    function cancelArgs() {
        pendingCommand = null;
        filterCommands(searchInput ? searchInput.value : '');
    }

    function filterCommands(query) {
        query = (query || '').trim();
        if (!query) {
            // Build sectioned list: Recent, Contextual, All Commands
            visibleCommands = [];
            var shownNames = {};

            // Recent section
            var recentItems = [];
            for (var ri = 0; ri < recentCommands.length; ri++) {
                var recentCmd = allCommands.find(function(c) { return c.name === recentCommands[ri]; });
                if (recentCmd) recentItems.push(recentCmd);
            }
            if (recentItems.length > 0) {
                visibleCommands.push({ _section: 'Recent' });
                for (var ri2 = 0; ri2 < recentItems.length; ri2++) {
                    var rcmd = Object.assign({}, recentItems[ri2], { _recent: true });
                    visibleCommands.push(rcmd);
                    shownNames[rcmd.name] = true;
                }
            }

            // Contextual section
            var context = detectActiveContext();
            if (context) {
                var contextItems = allCommands.filter(function(c) {
                    return c.category === context && !shownNames[c.name];
                });
                if (contextItems.length > 0) {
                    visibleCommands.push({ _section: 'Suggested \u2014 ' + context });
                    for (var ci = 0; ci < contextItems.length; ci++) {
                        visibleCommands.push(contextItems[ci]);
                        shownNames[contextItems[ci].name] = true;
                    }
                }
            }

            // All commands section (remaining)
            var remaining = allCommands.filter(function(c) { return !shownNames[c.name]; });
            remaining.sort(function(a, b) { return a.name.localeCompare(b.name); });
            if (remaining.length > 0) {
                visibleCommands.push({ _section: 'All Commands' });
                for (var ai = 0; ai < remaining.length; ai++) {
                    visibleCommands.push(remaining[ai]);
                }
            }
        } else {
            // Score and sort by relevance
            var scored = [];
            for (var i = 0; i < allCommands.length; i++) {
                var s = scoreCommand(allCommands[i], query);
                if (s > 0) {
                    scored.push({ cmd: allCommands[i], score: s });
                }
            }
            scored.sort(function(a, b) { return b.score - a.score; });
            visibleCommands = scored.map(function(item) { return item.cmd; });
        }
        selectedIdx = 0;
        // In browse mode, skip section headers for initial selection
        while (selectedIdx < visibleCommands.length && visibleCommands[selectedIdx]._section) {
            selectedIdx++;
        }
        renderResults();
    }

    function openPalette() {
        isPaletteOpen = true;
        pendingCommand = null;
        if (overlay) {
            overlay.style.display = 'flex';
            overlay.classList.add('open');
        }
        if (searchInput) {
            searchInput.value = '';
            searchInput.focus();
        }
        filterCommands('');
        // Fetch fresh options in background
        fetchOptions();
    }

    function closePalette() {
        isPaletteOpen = false;
        pendingCommand = null;
        if (overlay) {
            overlay.classList.remove('open');
            overlay.style.display = 'none';
        }
        if (searchInput) {
            searchInput.value = '';
        }
        visibleCommands = [];
        if (resultsDiv) {
            resultsDiv.innerHTML = '';
        }
    }

    function selectCommand(cmdName, cmdArgs) {
        // If command needs args, show args input
        if (cmdArgs) {
            var cmd = allCommands.find(function(c) { return c.name === cmdName; });
            if (cmd) {
                pendingCommand = cmd;
                // Make sure options are loaded before rendering
                if (cmd.argType && !cachedOptions) {
                    fetchOptions().then(function() {
                        renderResults();
                    });
                } else {
                    renderResults();
                }
                return;
            }
        }
        // No args needed, run directly
        runCommand(cmdName);
    }

    function runCommand(cmdName) {
        if (executionLock) {
            console.log('Execution locked, ignoring');
            return;
        }
        if (!cmdName) {
            console.log('No command name');
            return;
        }

        // Close palette FIRST before anything else
        closePalette();

        // Save to recent commands history
        // Extract base command name (without args) for history
        var baseName = cmdName.split(' ').slice(0, 3).join(' ');
        var matchedCmd = allCommands.find(function(c) { return cmdName.indexOf(c.name) === 0; });
        saveRecentCommand(matchedCmd ? matchedCmd.name : baseName);

        executionLock = true;
        console.log('Running command:', cmdName);

        showToast('info', 'Running...', 'gt ' + cmdName);

        var payload = { command: cmdName };
        // Include confirmed flag if the command requires server-side confirmation
        if (matchedCmd && matchedCmd.confirm) {
            payload.confirmed = true;
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Success', 'gt ' + cmdName);
                if (data.output && data.output.trim()) {
                    showOutput(cmdName, data.output);
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
                if (data.output) {
                    showOutput(cmdName, data.output);
                }
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
        })
        .finally(function() {
            // Unlock after 1 second to prevent double-clicks
            setTimeout(function() {
                executionLock = false;
            }, 1000);
        });
    }

    // SINGLE click handler for command palette
    resultsDiv.addEventListener('click', function(e) {
        var item = e.target.closest('.command-item');
        if (!item) return;

        e.preventDefault();
        e.stopPropagation();

        var cmdName = item.getAttribute('data-cmd-name');
        var cmdArgs = item.getAttribute('data-cmd-args');
        if (cmdName) {
            selectCommand(cmdName, cmdArgs);
        }
    });

    // Open palette button
    document.addEventListener('click', function(e) {
        if (e.target.closest('#open-palette-btn')) {
            e.preventDefault();
            openPalette();
            return;
        }
        // Click on overlay background closes palette
        if (e.target === overlay) {
            closePalette();
        }
    });

    // Keyboard handling
    document.addEventListener('keydown', function(e) {
        // Cmd+K or Ctrl+K toggles palette
        if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
            e.preventDefault();
            if (isPaletteOpen) {
                closePalette();
            } else {
                openPalette();
            }
            return;
        }

        // Escape closes expanded panels when palette is not open
        if (!isPaletteOpen && e.key === 'Escape') {
            var expanded = document.querySelector('.panel.expanded');
            if (expanded) {
                e.preventDefault();
                expanded.classList.remove('expanded');
                var expandBtn = expanded.querySelector('.expand-btn');
                if (expandBtn) expandBtn.textContent = 'Expand';
                window.pauseRefresh = false;
                return;
            }
        }

        // Rest only when palette is open
        if (!isPaletteOpen) return;

        // If in args mode, let the args input handle keys
        if (pendingCommand) return;

        if (e.key === 'Escape') {
            e.preventDefault();
            closePalette();
            return;
        }

        if (e.key === 'ArrowDown') {
            e.preventDefault();
            if (visibleCommands.length > 0) {
                var next = selectedIdx + 1;
                // Skip section headers
                while (next < visibleCommands.length && visibleCommands[next]._section) next++;
                if (next < visibleCommands.length) selectedIdx = next;
                renderResults();
            }
            return;
        }

        if (e.key === 'ArrowUp') {
            e.preventDefault();
            var prev = selectedIdx - 1;
            // Skip section headers
            while (prev >= 0 && visibleCommands[prev]._section) prev--;
            if (prev >= 0) selectedIdx = prev;
            renderResults();
            return;
        }

        if (e.key === 'Enter') {
            e.preventDefault();
            var selected = visibleCommands[selectedIdx];
            if (selected && !selected._section) {
                selectCommand(selected.name, selected.args);
            }
            return;
        }
    });

    // Input filtering
    searchInput.addEventListener('input', function() {
        filterCommands(searchInput.value);
    });

})();
