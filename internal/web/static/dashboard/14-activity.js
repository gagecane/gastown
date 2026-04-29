// Dashboard module: activity-timeline-filters
// Extracted from dashboard.js lines 2924-3036.
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


    function initTimelineFilters() {
        var timeline = document.getElementById('activity-timeline');
        if (!timeline) return;

        var entries = timeline.querySelectorAll('.tl-entry');
        var rigFilter = document.getElementById('tl-rig-filter');
        var agentFilter = document.getElementById('tl-agent-filter');
        var emptyMsg = document.getElementById('tl-empty-filtered');

        // Collect unique rigs and agents for dropdowns
        var rigs = {};
        var agents = {};
        // Use actual rig names from the Rigs panel, not data-rig attributes
        // (which include non-rig values like 'mayor', 'deacon', 'hq')
        document.querySelectorAll('.rig-name').forEach(function(el) {
            var name = el.textContent.trim();
            if (name) rigs[name] = true;
        });
        entries.forEach(function(entry) {
            var agent = entry.getAttribute('data-agent');
            if (agent) agents[agent] = true;
        });

        // Populate rig dropdown
        if (rigFilter) {
            Object.keys(rigs).sort().forEach(function(rig) {
                var opt = document.createElement('option');
                opt.value = rig;
                opt.textContent = rig;
                rigFilter.appendChild(opt);
            });
        }

        // Populate agent dropdown
        if (agentFilter) {
            Object.keys(agents).sort().forEach(function(agent) {
                var opt = document.createElement('option');
                opt.value = agent;
                opt.textContent = agent;
                agentFilter.appendChild(opt);
            });
        }

        // Current filter state
        var activeCategory = 'all';

        function applyFilters() {
            var selectedRig = rigFilter ? rigFilter.value : 'all';
            var selectedAgent = agentFilter ? agentFilter.value : 'all';
            var visibleCount = 0;

            entries.forEach(function(entry) {
                var show = true;

                if (activeCategory !== 'all' && entry.getAttribute('data-category') !== activeCategory) {
                    show = false;
                }
                if (selectedRig !== 'all' && entry.getAttribute('data-rig') !== selectedRig) {
                    show = false;
                }
                if (selectedAgent !== 'all' && entry.getAttribute('data-agent') !== selectedAgent) {
                    show = false;
                }

                if (show) {
                    entry.classList.remove('tl-hidden');
                    visibleCount++;
                } else {
                    entry.classList.add('tl-hidden');
                }
            });

            if (emptyMsg) {
                emptyMsg.style.display = visibleCount === 0 ? 'block' : 'none';
            }
        }

        // Category filter buttons
        document.addEventListener('click', function(e) {
            var btn = e.target.closest('.tl-filter-btn');
            if (!btn) return;
            if (btn.getAttribute('data-filter') !== 'category') return;

            // Update active state
            var group = btn.closest('.tl-filter-group');
            if (group) {
                group.querySelectorAll('.tl-filter-btn').forEach(function(b) {
                    b.classList.remove('active');
                });
            }
            btn.classList.add('active');
            activeCategory = btn.getAttribute('data-value');
            applyFilters();
        });

        // Dropdown filters
        if (rigFilter) {
            rigFilter.addEventListener('change', applyFilters);
        }
        if (agentFilter) {
            agentFilter.addEventListener('change', applyFilters);
        }
    }

    // Init on page load
    initTimelineFilters();

    // Re-init after HTMX swaps
    document.body.addEventListener('htmx:afterSwap', function() {
        initTimelineFilters();
    });

})();
