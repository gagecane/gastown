// Package liveness provides a single, shared PID-liveness probe.
//
// Gastown checks "is this process still alive?" in ~20 places (daemon pid
// files, dolt-server pid files, polecat-admission reservations, tmux pane
// PIDs, ACP proxies, lock files, …). Each site historically reimplemented the
// same "kill -0" probe, and the implementations drifted: some treated a
// permission error (EPERM / ACCESS_DENIED) as dead, others as alive. This
// package collapses them onto one leaf with one contract.
//
// Contract for PIDAlive(pid):
//   - pid <= 0                    -> false (not a real PID)
//   - process is running          -> true
//   - process is gone             -> false
//   - process exists but we lack
//     permission to signal it      -> true
//
// The "exists-but-denied -> alive" choice is deliberate and conservative: a
// process we are not allowed to signal still exists, so reporting it dead
// would risk reaping state (admission reservations, pid files, beads) that
// belongs to a live process. This matches the behavior every Windows
// implementation already had and the documented choice in the deacon's
// stale-spawning sweeper.
package liveness
