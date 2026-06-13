package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	reaperDB       string
	reaperHost     string
	reaperPort     int
	reaperMaxAge   string
	reaperPurgeAge string
	reaperMailAge  string
	reaperStaleAge string
	reaperDBDelay  string
	reaperDryRun   bool
	reaperJSON     bool
)

func reaperDatabaseNames() []string {
	if reaperDB == "" {
		return reaper.DiscoverDatabases(reaperHost, reaperPort)
	}
	parts := strings.Split(reaperDB, ",")
	databases := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			databases = append(databases, name)
		}
	}
	return databases
}

func waitBeforeReaperDatabase(index int) error {
	if index == 0 {
		return nil
	}
	delay, err := time.ParseDuration(reaperDBDelay)
	if err != nil {
		return fmt.Errorf("invalid --db-delay: %w", err)
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	return nil
}

// mailReapRow is the per-database view a mail-reap command prints. It flattens
// the differing *MailResult types (HookedMailResult/OpenMailResult) to the
// common fields the report renders, plus the result's "remain" count, whose
// field name differs per type.
type mailReapRow struct {
	database  string
	dryRun    bool
	closed    int
	remain    int
	entries   []reaper.ClosedEntry
	anomalies []reaper.Anomaly
}

// printMailReapReport renders the human (non-JSON) output shared by the
// reap-hooked-mail and reap-open-mail commands. entryWord/remainWord label the
// per-entry and per-db remain counts (e.g. "hooked"/"open"), noun names the
// bead kind ("hooked mail"), and summaryTitle prefixes the multi-db summary
// line ("Reap-hooked-mail").
func printMailReapReport(rows []mailReapRow, entryWord, noun, remainWord, summaryTitle string) {
	var totalClosed, totalRemain int
	for _, r := range rows {
		prefix := ""
		verb := "closed"
		if r.dryRun {
			prefix = "[DRY RUN] would "
			verb = "close"
		}
		for _, entry := range r.entries {
			fmt.Printf("  %s %s (%dd %s, db:%s)\n",
				entry.ID, entry.Title, entry.AgeDays, entryWord, entry.Database)
		}
		fmt.Printf("%s: %s%s %d %s bead(s), %d remain %s\n",
			r.database, prefix, verb, r.closed, noun, r.remain, remainWord)
		for _, a := range r.anomalies {
			fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
		}
		totalClosed += r.closed
		totalRemain += r.remain
	}
	if len(rows) > 1 {
		prefix := ""
		if reaperDryRun {
			prefix = "[DRY RUN] "
		}
		fmt.Printf("\n%s%s summary (%d databases): closed %d, %d %s remain\n",
			prefix, summaryTitle, len(rows), totalClosed, totalRemain, entryWord)
	}
}

// runMailReapCommand is the shared RunE body for the reap-hooked-mail and
// reap-open-mail commands. It parses ttlStr, reaps every database via op, and
// emits JSON or the human report. label names the operation for per-db error
// messages; entryWord/noun/remainWord/summaryTitle are the report labels (see
// printMailReapReport); toRow flattens each result to a mailReapRow.
func runMailReapCommand[T any](ttlStr, label, entryWord, noun, remainWord, summaryTitle string, reap func(db *sql.DB, dbName string, ttl time.Duration) (T, error), toRow func(T) mailReapRow) error {
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid --ttl: %w", err)
	}

	results, _ := reaperPerDBResults(label, 10*time.Second, false,
		func(db *sql.DB, dbName string) (T, error) {
			return reap(db, dbName, ttl)
		})

	if reaperJSON {
		fmt.Println(reaper.FormatJSON(results))
		return nil
	}
	rows := make([]mailReapRow, 0, len(results))
	for _, r := range results {
		rows = append(rows, toRow(r))
	}
	printMailReapReport(rows, entryWord, noun, remainWord, summaryTitle)
	return nil
}

// reaperPerDBResults runs op against every discovered/--db database, handling
// the connect → reaper-schema check → close scaffolding that every reaper
// subcommand shares. Databases that fail validation, connection, or the schema
// check (or that lack the reaper schema) are skipped with a stderr note; op is
// invoked only for databases that pass. op receives an open *sql.DB (closed by
// this helper after op returns) and the database name, and returns the result
// to collect plus an error. label names the operation in error messages (e.g.
// "reap-hooked-mail"). connTimeout sets the OpenDB read/write timeout.
//
// When paced is true the inter-database delay (--db-delay) is applied before
// each database after the first, as the standalone reap/auto-close/purge
// commands require; a delay parse error aborts the run and is returned with the
// results collected so far. Otherwise the returned error is always nil.
//
// Collects and returns every non-error result. op errors are logged to stderr
// and that database is skipped, matching the prior per-command behavior.
func reaperPerDBResults[T any](label string, connTimeout time.Duration, paced bool, op func(db *sql.DB, dbName string) (T, error)) ([]T, error) {
	databases := reaperDatabaseNames()

	var results []T
	for i, dbName := range databases {
		if paced {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return results, err
			}
		}
		if err := reaper.ValidateDBName(dbName); err != nil {
			fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
			continue
		}

		db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, connTimeout, connTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
			continue
		}

		if ok, err := reaper.HasReaperSchema(db); err != nil {
			fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
			db.Close()
			continue
		} else if !ok {
			db.Close()
			continue
		}

		result, err := op(db, dbName)
		db.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s error: %v\n", dbName, label, err)
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

var reaperCmd = &cobra.Command{
	Use:     "reaper",
	GroupID: GroupServices,
	Short:   "Wisp and issue cleanup operations (Dog-callable helpers)",
	Long: `Execute wisp reaper operations against Dolt databases.

These subcommands are the callable helper functions for the mol-dog-reaper
formula. They execute SQL operations but leave eligibility decisions to the
Dog agent or daemon orchestrator.

When run by a Dog:
  gt reaper scan --db=gastown                  # Discover candidates
  gt reaper reap --db=gastown                  # Close stale wisps
  gt reaper purge --db=gastown                 # Delete old closed wisps + mail
  gt reaper auto-close --db=gastown            # Close stale issues
  gt reaper close-plugin-receipts --db=gastown # Close stale plugin-run wisps (fast-track)`,
	RunE: requireSubcommand,
}

var reaperDatabasesCmd = &cobra.Command{
	Use:   "databases",
	Short: "List databases available for reaping",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbs := reaper.DiscoverDatabases(reaperHost, reaperPort)
		if reaperJSON {
			fmt.Println(reaper.FormatJSON(dbs))
		} else {
			for _, db := range dbs {
				fmt.Println(db)
			}
		}
		return nil
	},
}

var reaperScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan databases for reaper candidates",
	Long: `Count reap, purge, auto-close, and mail candidates in databases.

When --db is provided, scans a single database. When omitted, auto-discovers
all databases on the Dolt server and scans each one, printing a summary.

Returns counts and anomaly detection results without modifying any data.
The Dog uses this to understand the state before deciding what to reap.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}

		databases := reaperDatabaseNames()

		var results []*reaper.ScanResult
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.Scan(db, dbName, maxAge, purgeAge, mailAge, staleAge)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: scan error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalReap, totalPurge, totalMail, totalStale, totalOpen int
			for _, r := range results {
				fmt.Printf("Database: %s\n", r.Database)
				fmt.Printf("  Reap candidates:  %d\n", r.ReapCandidates)
				fmt.Printf("  Purge candidates: %d\n", r.PurgeCandidates)
				fmt.Printf("  Mail candidates:  %d\n", r.MailCandidates)
				fmt.Printf("  Stale candidates: %d\n", r.StaleCandidates)
				fmt.Printf("  Open wisps:       %d\n", r.OpenWisps)
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalReap += r.ReapCandidates
				totalPurge += r.PurgeCandidates
				totalMail += r.MailCandidates
				totalStale += r.StaleCandidates
				totalOpen += r.OpenWisps
			}
			if len(results) > 1 {
				fmt.Printf("\nScan summary (%d databases):\n", len(results))
				fmt.Printf("  Reap candidates:  %d\n", totalReap)
				fmt.Printf("  Purge candidates: %d\n", totalPurge)
				fmt.Printf("  Mail candidates:  %d\n", totalMail)
				fmt.Printf("  Stale candidates: %d\n", totalStale)
				fmt.Printf("  Open wisps:       %d\n", totalOpen)
			}
		}
		return nil
	},
}

var reaperReapCmd = &cobra.Command{
	Use:   "reap",
	Short: "Close stale wisps past max-age",
	Long: `Close wisps that are past the max-age threshold and whose parent
molecule is already closed (or missing/orphaned).

When --db is provided, reaps a single database. When omitted, auto-discovers
all databases on the Dolt server and reaps each one.

Returns the count of reaped wisps. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}

		results, err := reaperPerDBResults("reap", 10*time.Second, true,
			func(db *sql.DB, dbName string) (*reaper.ReapResult, error) {
				return reaper.Reap(db, dbName, maxAge, reaperDryRun)
			})
		if err != nil {
			return err
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalReaped, totalOpen int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				fmt.Printf("%s: %sreaped %d wisps, %d open remain\n",
					r.Database, prefix, r.Reaped, r.OpenRemain)
				totalReaped += r.Reaped
				totalOpen += r.OpenRemain
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sReap summary (%d databases): reaped %d wisps, %d open remain\n",
					prefix, len(results), totalReaped, totalOpen)
				if totalOpen > reaper.DefaultAlertThreshold {
					fmt.Fprintf(os.Stderr, "WARNING: %d open wisps exceed alert threshold (%d)\n",
						totalOpen, reaper.DefaultAlertThreshold)
				}
			}
		}
		return nil
	},
}

var reaperPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete old closed wisps and mail",
	Long: `Delete closed wisps past the purge-age threshold and closed mail
past the mail-age threshold. Irreversible operation.

When --db is provided, purges a single database. When omitted, auto-discovers
all databases on the Dolt server and purges each one.

Returns counts of purged rows. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}

		results, err := reaperPerDBResults("purge", 30*time.Second, true,
			func(db *sql.DB, dbName string) (*reaper.PurgeResult, error) {
				return reaper.Purge(db, dbName, purgeAge, mailAge, reaperDryRun)
			})
		if err != nil {
			return err
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalWisps, totalMail int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				fmt.Printf("%s: %spurged %d wisps, %d mail\n",
					r.Database, prefix, r.WispsPurged, r.MailPurged)
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalWisps += r.WispsPurged
				totalMail += r.MailPurged
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sPurge summary (%d databases): purged %d wisps, %d mail\n",
					prefix, len(results), totalWisps, totalMail)
			}
		}
		return nil
	},
}

var reaperAutoCloseCmd = &cobra.Command{
	Use:   "auto-close",
	Short: "Close stale issues past stale-age",
	Long: `Close issues open with no updates past the stale-age threshold.
Excludes P0/P1 priority, epics, and issues with active dependencies.

When --db is provided, auto-closes in a single database. When omitted,
auto-discovers all databases on the Dolt server and auto-closes in each one.

Returns the count of closed issues. Use --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}

		results, err := reaperPerDBResults("auto-close", 10*time.Second, true,
			func(db *sql.DB, dbName string) (*reaper.AutoCloseResult, error) {
				return reaper.AutoClose(db, dbName, staleAge, reaperDryRun)
			})
		if err != nil {
			return err
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalClosed int
			for _, r := range results {
				prefix := ""
				if r.DryRun {
					prefix = "[DRY RUN] would "
				}
				for _, entry := range r.ClosedEntries {
					fmt.Printf("  %s %s (%dd stale, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				fmt.Printf("%s: %sauto-closed %d stale issues\n",
					r.Database, prefix, r.Closed)
				totalClosed += r.Closed
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sAuto-close summary (%d databases): auto-closed %d stale issues\n",
					prefix, len(results), totalClosed)
			}
		}
		return nil
	},
}

var reaperHookedMailTTL string

var reaperReapHookedMailCmd = &cobra.Command{
	Use:   "reap-hooked-mail",
	Short: "Close stale hooked mail beads (ttl-expired)",
	Long: `Close mail beads stuck in the 'hooked' state past the TTL threshold.

HANDOFF and other mail beads are hooked for successor sessions to consume.
If a successor never runs 'gt prime --hook' (session died, rerouted, or the
bead is orphaned), the hook stays forever and accumulates as dead-letter.
This command closes such beads with reason "ttl-expired".

Excludes:
  - Agent heartbeat beads (issue_type='agent')
  - Pinned beads (status != 'hooked')
  - Beads labeled gt:standing-orders, gt:keep, gt:role, or gt:rig

When --db is provided, operates on a single database. When omitted,
auto-discovers all databases on the Dolt server.

Use --dry-run to preview closures without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMailReapCommand(reaperHookedMailTTL, "reap-hooked-mail", "hooked", "hooked mail", "hooked", "Reap-hooked-mail",
			func(db *sql.DB, dbName string, ttl time.Duration) (*reaper.HookedMailResult, error) {
				return reaper.ReapHookedMail(db, dbName, ttl, reaperDryRun)
			},
			func(r *reaper.HookedMailResult) mailReapRow {
				return mailReapRow{
					database:  r.Database,
					dryRun:    r.DryRun,
					closed:    r.Closed,
					remain:    r.HookedRemain,
					entries:   r.ClosedEntries,
					anomalies: r.Anomalies,
				}
			})
	},
}

var reaperOpenMailTTL string

var reaperReapOpenMailCmd = &cobra.Command{
	Use:   "reap-open-mail",
	Short: "Close stale open (un-hooked) mail beads (ttl-expired)",
	Long: `Close mail beads stuck in the 'open' or 'in_progress' state past the TTL threshold.

HANDOFF and other coordination mail sent via 'gt mail send' (from witness,
mayor, deacon roles, stuck-agent-dog plugin, etc.) create beads with
priority=1 (HIGH) and status='open'. The standard AutoClose reaper excludes
P0/P1 beads, so these accumulate forever and pollute 'bd ready'. This
command closes stale open mail with reason "ttl-expired", independent of
priority.

Excludes:
  - Agent heartbeat beads (issue_type='agent')
  - Hooked, pinned, or closed beads (filtered by the WHERE clause)
  - Beads labeled gt:standing-orders, gt:keep, gt:role, or gt:rig
  - Beads with a live consumer_bead_id (per ConsumerAliveClause, gu-ub1l)

When --db is provided, operates on a single database. When omitted,
auto-discovers all databases on the Dolt server.

Use --dry-run to preview closures without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMailReapCommand(reaperOpenMailTTL, "reap-open-mail", "open", "open mail", "open", "Reap-open-mail",
			func(db *sql.DB, dbName string, ttl time.Duration) (*reaper.OpenMailResult, error) {
				return reaper.ReapOpenMail(db, dbName, ttl, reaperDryRun)
			},
			func(r *reaper.OpenMailResult) mailReapRow {
				return mailReapRow{
					database:  r.Database,
					dryRun:    r.DryRun,
					closed:    r.Closed,
					remain:    r.OpenRemain,
					entries:   r.ClosedEntries,
					anomalies: r.Anomalies,
				}
			})
	},
}

var reaperProcessedMailTTL string

var reaperReapProcessedMailCmd = &cobra.Command{
	Use:   "reap-processed-mail",
	Short: "Close processed (read/acked) message & escalation beads",
	Long: `Close message and escalation beads that have been PROCESSED (read or
acknowledged) and are older than the TTL, with reason "processed".

Every escalation/mail to the mayor creates a permanent bead in the hq DB
(labeled gt:message or gt:escalation). 'gt mail mark-read' adds 'read' +
'delivery:acked'; 'gt escalate ack' adds 'acked' — but neither closes the
bead. So fully-processed notifications stay status='open' forever, growing
the DB and polluting 'bd ready' / 'bd list'. This command closes those
acted-on notifications after a short audit window.

Only PROCESSED beads are swept — an un-acked escalation stays open so it
still demands attention. This complements reap-open-mail (blind TTL on
gt:message only, never touches gt:escalation).

Excludes:
  - Un-processed beads (no read/delivery:acked/acked label)
  - Agent heartbeat beads (issue_type='agent')
  - Hooked, pinned, or closed beads (filtered by the WHERE clause)
  - Beads labeled gt:standing-orders, gt:keep, gt:role, or gt:rig
  - Beads with a live consumer_bead_id (per ConsumerAliveClause, gu-ub1l)

When --db is provided, operates on a single database. When omitted,
auto-discovers all databases on the Dolt server.

Use --dry-run to preview closures without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, err := time.ParseDuration(reaperProcessedMailTTL)
		if err != nil {
			return fmt.Errorf("invalid --ttl: %w", err)
		}

		results, _ := reaperPerDBResults("reap-processed-mail", 10*time.Second, false,
			func(db *sql.DB, dbName string) (*reaper.ProcessedMailResult, error) {
				result, err := reaper.ReapProcessedMail(db, dbName, ttl, reaperDryRun)
				if err != nil {
					return nil, err
				}
				// Also drain the dolt-ignored wisps copies (gu-2md8k): the same
				// processed message/escalation beads accumulate there and are
				// what the open-wisp alert counts. Merge the wisp closures into
				// the same result so the per-db report reflects both tables.
				wispResult, werr := reaper.ReapProcessedWispMail(db, dbName, ttl, reaperDryRun)
				if werr != nil {
					fmt.Fprintf(os.Stderr, "%s: reap-processed-wisp-mail error: %v\n", dbName, werr)
				} else {
					result.Closed += wispResult.Closed
					result.ProcessedRemain += wispResult.ProcessedRemain
					result.ClosedEntries = append(result.ClosedEntries, wispResult.ClosedEntries...)
					result.Anomalies = append(result.Anomalies, wispResult.Anomalies...)
				}
				return result, nil
			})

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalClosed, totalRemain int
			for _, r := range results {
				prefix := ""
				verb := "closed"
				if r.DryRun {
					prefix = "[DRY RUN] would "
					verb = "close"
				}
				for _, entry := range r.ClosedEntries {
					fmt.Printf("  %s %s (%dd processed, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				fmt.Printf("%s: %s%s %d processed mail/escalation bead(s), %d remain open\n",
					r.Database, prefix, verb, r.Closed, r.ProcessedRemain)
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalClosed += r.Closed
				totalRemain += r.ProcessedRemain
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sReap-processed-mail summary (%d databases): closed %d, %d remain open\n",
					prefix, len(results), totalClosed, totalRemain)
			}
		}
		return nil
	},
}

var reaperPluginReceiptAge string

var reaperClosePluginReceiptsCmd = &cobra.Command{
	Use:   "close-plugin-receipts",
	Short: "Close stale plugin-run receipt wisps",
	Long: `Close open wisps labeled "type:plugin-run" older than --max-age.

These are transient run receipts created by deacon dog plugins and patrol
scripts (RESTART_POLECAT, stuck-agent-dog, dolt-backup, mol-dog-*, etc.).
They exist only for audit/cooldown-gate purposes and should be closed
shortly after creation. The standard reap path uses 24h max_age, which
lets receipts accumulate past the alert_threshold during normal-volume
daemon activity (gs-g9k).

Operates on the wisps/wisp_labels tables.

When --db is provided, operates on a single database. When omitted,
auto-discovers all databases on the Dolt server.

Use --dry-run to preview closures without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAge, err := time.ParseDuration(reaperPluginReceiptAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}

		databases := reaper.DiscoverDatabases(reaperHost, reaperPort)
		if reaperDB != "" {
			databases = strings.Split(reaperDB, ",")
		}

		var results []*reaper.ClosePluginReceiptResult
		for _, dbName := range databases {
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.ClosePluginReceipts(db, dbName, maxAge, reaperDryRun)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: close-plugin-receipts error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalClosed int
			for _, r := range results {
				prefix := ""
				verb := "closed"
				if r.DryRun {
					prefix = "[DRY RUN] would "
					verb = "close"
				}
				fmt.Printf("%s: %s%s %d plugin-run receipt(s)\n",
					r.Database, prefix, verb, r.Closed)
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalClosed += r.Closed
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sClose-plugin-receipts summary (%d databases): closed %d\n",
					prefix, len(results), totalClosed)
			}
		}
		return nil
	},
}

var reaperFlushWispsCmd = &cobra.Command{
	Use:   "flush-wisps",
	Short: "Flush the dolt_ignored wisp_* working set to HEAD (gu-tqtwt)",
	Long: `Commit the dolt_ignored wisp tables (wisps, wisp_events, wisp_labels,
wisp_comments, wisp_dependencies) to Dolt HEAD.

Background: bd commits the issue tables on every op, but the wisp tables are
dolt_ignored, so bd's DOLT_COMMIT('-Am') never stages them. Their churn
accumulates unbounded in the Dolt working set. bd's pre-migration dirty-table
guard reads the raw dolt_diff('HEAD','WORKING') — which DOES see ignored tables
— so once the backlog is large enough, schema-init aborts ("pending schema
migrations alter pre-existing dirty tables") and every --json/capacity query
fails. The deadlock is self-sustaining: bd's own commit path runs the same
guard on connect, so bd cannot flush the working set it needs to clear the
guard (gu-tqtwt).

This command runs over a raw MySQL connection that never invokes bd's
schema-init guard, force-staging each wisp table (DOLT_ADD --force) and
committing. It is the bd-native-free escape hatch for the deadlock, and the
daemon runs it every reaper cycle to bound the backlog so no rig crosses the
threshold.

When --db is provided, operates on a single database. When omitted,
auto-discovers all databases on the Dolt server.

Use --dry-run to preview what would be flushed without committing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		databases := reaper.DiscoverDatabases(reaperHost, reaperPort)
		if reaperDB != "" {
			databases = strings.Split(reaperDB, ",")
		}

		var results []*reaper.FlushWispResult
		for _, dbName := range databases {
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 30*time.Second, 30*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Fprintf(os.Stderr, "%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				db.Close()
				continue
			}

			result, err := reaper.FlushWispWorkingSet(db, dbName, reaperDryRun)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: flush-wisps error: %v\n", dbName, err)
				continue
			}
			results = append(results, result)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(results))
		} else {
			var totalFlushed int
			for _, r := range results {
				prefix := ""
				verb := "flushed"
				if r.DryRun {
					prefix = "[DRY RUN] would "
					verb = "flush"
				}
				if r.Flushed > 0 {
					fmt.Printf("%s: %s%s %d pending wisp row change(s) across %v\n",
						r.Database, prefix, verb, r.Flushed, r.Tables)
				}
				for _, a := range r.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
				totalFlushed += r.Flushed
			}
			if len(results) > 1 {
				prefix := ""
				if reaperDryRun {
					prefix = "[DRY RUN] "
				}
				fmt.Printf("\n%sFlush-wisps summary (%d databases): flushed %d pending row change(s)\n",
					prefix, len(results), totalFlushed)
			}
		}
		return nil
	},
}

var reaperScrubActiveMRCmd = &cobra.Command{
	Use:   "scrub-active-mr",
	Short: "Clear stale active_mr refs on agent beads (gu-dhqm)",
	Long: `Scan every agent bead and clear active_mr fields whose referenced
merge-request and source issue are both terminal.

Background: ` + "`active_mr`" + ` is set by ` + "`gt done`" + ` and cleared by exactly one
path — the refinery engineer's post-merge happy path. Every other lifecycle
end (rebase-after-push, force-close, sibling-MR landing first, wisp TTL-reap)
leaves the field dangling, where it eventually combines with cleanup_status
drift to produce permanent ` + "`idle-recovery-needed`" + ` verdicts that hold
scheduler slots.

This command runs the same ` + "`polecat.AssessActiveMR`" + ` classifier that the
on-demand recovery path uses, and clears the field when the assessment proves
both the MR and the source issue are terminal.

Preserves polecats with cleanup_status in {has_uncommitted, has_stash,
has_unpushed} — the dangling ref is intentional audit trail for the human
triage path (gc-eysed).

Operates on the town database where agent beads live; the --db / --host /
--port flags are used only for context discovery and are otherwise ignored.

Use --dry-run to preview clears without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding town root: %w", err)
		}
		bd := beads.New(townRoot).ForAgentBead()

		result, err := reaper.ScrubStaleActiveMR(bd, reaperDryRun)
		if err != nil {
			return fmt.Errorf("scrub active_mr: %w", err)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(result))
			return nil
		}

		prefix := ""
		verb := "cleared"
		if result.DryRun {
			prefix = "[DRY RUN] would "
			verb = "clear"
		}
		for _, entry := range result.ClearedEntries {
			fmt.Printf("  %s active_mr=%s mr_status=%s source=%s\n",
				entry.AgentBeadID, entry.ActiveMR, entry.MRStatus, entry.SourceIssue)
		}
		fmt.Printf("scrub-active-mr: scanned=%d had_active_mr=%d %s%s=%d preserved_wip=%d still_pending=%d\n",
			result.Scanned, result.HadActiveMR, prefix, verb, result.Cleared,
			result.PreservedWIP, result.StillPending)
		for _, a := range result.Anomalies {
			fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
		}
		return nil
	},
}

var reaperReconcileOrphansCmd = &cobra.Command{
	Use:   "reconcile-orphans",
	Short: "Complete interrupted post-merge reconciles (gu-7igu8)",
	Long: `Scan every agent bead and complete the refinery's post-merge reconcile
for any whose active_mr points at a proven-merged MR with a still-non-terminal
source issue.

Background: the refinery's post-merge sequence — close MR → close source issue
→ unhook bead → enable polecat reap — is NOT atomic. When the refinery is
interrupted mid-reconcile (latch/restart, or it proceeds to the next MR before
finishing) AFTER the MR merged but BEFORE the source issue closed, the source
issue is left non-terminal (typically HOOKED on a now-dead polecat). The work
is provably on main, but the stale HOOKED bead blocks 'gt polecat nuke' and can
mislead dispatch.

This command force-closes each such orphaned source issue with a "Merged in
<mr>" reason (transitioning it out of HOOKED) and clears the leaked
awaiting_refinery_merge label — completing exactly what the refinery's PostMerge
path would have done.

Safety: ONLY proven-merged MRs (close_reason=merged or a merge_commit SHA)
trigger a source close. Rejected/superseded/conflict or missing MR beads are
skipped — the work did not land. Polecats preserving human WIP (cleanup_status
has_uncommitted/has_stash/has_unpushed) are skipped (gc-eysed).

Operates on the town database where agent beads live; source-issue closes route
to the owning rig database via bd prefix routing.

Use --dry-run to preview closes without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding town root: %w", err)
		}
		bd := beads.New(townRoot).ForAgentBead()

		result, err := reaper.ReconcileMergedOrphans(bd, reaperDryRun)
		if err != nil {
			return fmt.Errorf("reconcile orphans: %w", err)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(result))
			return nil
		}

		prefix := ""
		verb := "reconciled"
		if result.DryRun {
			prefix = "[DRY RUN] would "
			verb = "reconcile"
		}
		for _, entry := range result.ReconciledEntries {
			fmt.Printf("  %s active_mr=%s source=%s\n",
				entry.AgentBeadID, entry.ActiveMR, entry.SourceIssue)
		}
		fmt.Printf("reconcile-orphans: scanned=%d had_active_mr=%d %s%s=%d preserved_wip=%d\n",
			result.Scanned, result.HadActiveMR, prefix, verb, result.Reconciled, result.PreservedWIP)
		for _, a := range result.Anomalies {
			fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
		}
		return nil
	},
}

var reaperReconcileOrphansGitCmd = &cobra.Command{
	Use:   "reconcile-orphans-git",
	Short: "Complete interrupted post-merge reconciles via git evidence (gu-hrweu)",
	Long: `Scan every source issue still carrying the awaiting_refinery_merge label and
complete the refinery's interrupted post-merge reconcile for any whose merge is
provable by a commit citing the bead ID on its target branch.

Background: the agent-bead reconcile ('reconcile-orphans', gu-7igu8) proves a
merge by reading the MR wisp bead the agent bead's active_mr points at. A
competing reaper cycle can destroy BOTH artifacts first — 'gt reaper purge'
deletes the MR wisp and scrub-active-mr clears active_mr — leaving the merged
work unprovable via beads, so the agent-bead pass skips it forever and the
source issue stays non-terminal, freezing any dependent convoy.

This command anchors on the two artifacts the race CANNOT destroy: the source
issue's awaiting_refinery_merge label and the merged commit on the target
branch (which cites the bead ID). For each labeled non-terminal source issue
whose work is provably on the target branch, it force-closes the issue and
clears the leaked label — exactly what the refinery's PostMerge path would have
done.

Safety: a close happens ONLY when git PROVES the merge (a citing commit on the
target branch). When git cannot verify (no worktree, git error), the bead is
left open — absence of proof is never proof. Already-terminal source issues are
skipped (idempotent).

Aggregates source issues across all rig databases; closes route to the owning
rig DB via bd prefix routing.

Use --dry-run to preview closes without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding town root: %w", err)
		}
		adapter := newOrphanGitReconcileAdapter(townRoot)

		result, err := reaper.ReconcileMergedOrphansByGitEvidence(adapter, adapter, reaperDryRun)
		if err != nil {
			return fmt.Errorf("reconcile orphans (git): %w", err)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(result))
			return nil
		}

		prefix := ""
		verb := "reconciled"
		if result.DryRun {
			prefix = "[DRY RUN] would "
			verb = "reconcile"
		}
		for _, entry := range result.ReconciledEntries {
			fmt.Printf("  %s (git evidence: citing commit on target branch)\n", entry.SourceIssue)
		}
		fmt.Printf("reconcile-orphans-git: scanned=%d %s%s=%d not_yet_merged=%d unverified=%d already_terminal=%d\n",
			result.Scanned, prefix, verb, result.Reconciled, result.NotYetMerged, result.Unverified, result.AlreadyTerminal)
		for _, a := range result.Anomalies {
			fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
		}
		return nil
	},
}

var reaperScrubDanglingFKCmd = &cobra.Command{
	Use:   "scrub-dangling-fk",
	Short: "Clear dangling mr_id/hook_bead refs on agent beads (gu-96uxo)",
	Long: `Scan every agent bead and clear ` + "`mr_id`" + ` and ` + "`hook_bead`" + ` fields whose
referenced bead no longer exists (the signature of a TTL-reaped or purged wisp).

Background: when the wisp reaper compacts an ephemeral bead, agent beads that
hold a foreign-key reference to it (` + "`mr_id`, `hook_bead`" + `) are left pointing at
an ID that no longer resolves. Those dangling pointers block downstream
automation — refinery dispatch reads them as "still working an MR" — and only
surface when the consumer escalates after N empty cycles (gu-96uxo).

This is the complement to ` + "`scrub-active-mr`" + ` (gu-dhqm), which already covers the
` + "`active_mr`" + ` field via the AssessActiveMR classifier. This command deliberately
does NOT touch active_mr.

Existence-only semantics: a referent that still exists (at any status) is left
untouched — only a MISSING referent is cleared. Fail-closed on lookup errors
other than not-found, so a flaky Dolt connection never produces spurious clears.

Preserves polecats with cleanup_status in {has_uncommitted, has_stash,
has_unpushed} — the dangling refs are intentional audit trail (gc-eysed).

Operates on the town database where agent beads live; the --db / --host /
--port flags are used only for context discovery and are otherwise ignored.

Use --dry-run to preview clears without applying them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding town root: %w", err)
		}
		bd := beads.New(townRoot).ForAgentBead()

		result, err := reaper.ScrubDanglingFKRefs(bd, reaperDryRun)
		if err != nil {
			return fmt.Errorf("scrub dangling fk: %w", err)
		}

		if reaperJSON {
			fmt.Println(reaper.FormatJSON(result))
			return nil
		}

		prefix := ""
		verb := "cleared"
		if result.DryRun {
			prefix = "[DRY RUN] would "
			verb = "clear"
		}
		for _, entry := range result.ClearedEntries {
			fmt.Printf("  %s %s=%s (referent missing)\n",
				entry.AgentBeadID, entry.Field, entry.Referent)
		}
		fmt.Printf("scrub-dangling-fk: scanned=%d had_mr_id=%d had_hook_bead=%d %s%s mr_id=%d hook_bead=%d preserved_wip=%d\n",
			result.Scanned, result.HadMRID, result.HadHookBead, prefix, verb,
			result.ClearedMRID, result.ClearedHookBead, result.PreservedWIP)
		for _, a := range result.Anomalies {
			fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
		}
		return nil
	},
}

var reaperRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run full reaper cycle across all databases",
	Long: `Execute a full reaper cycle: scan → reap → purge → auto-close → reap-hooked-mail → reap-open-mail → report.

This is the inline fallback for when Dog dispatch is unavailable.
Normally the daemon dispatches a Dog to execute the mol-dog-reaper formula.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		databases := reaperDatabaseNames()

		maxAge, err := time.ParseDuration(reaperMaxAge)
		if err != nil {
			return fmt.Errorf("invalid --max-age: %w", err)
		}
		purgeAge, err := time.ParseDuration(reaperPurgeAge)
		if err != nil {
			return fmt.Errorf("invalid --purge-age: %w", err)
		}
		mailAge, err := time.ParseDuration(reaperMailAge)
		if err != nil {
			return fmt.Errorf("invalid --mail-age: %w", err)
		}
		staleAge, err := time.ParseDuration(reaperStaleAge)
		if err != nil {
			return fmt.Errorf("invalid --stale-age: %w", err)
		}
		hookedMailTTL, err := time.ParseDuration(reaperHookedMailTTL)
		if err != nil {
			return fmt.Errorf("invalid --hooked-mail-ttl: %w", err)
		}
		openMailTTL, err := time.ParseDuration(reaperOpenMailTTL)
		if err != nil {
			return fmt.Errorf("invalid --open-mail-ttl: %w", err)
		}
		processedMailTTL, err := time.ParseDuration(reaperProcessedMailTTL)
		if err != nil {
			return fmt.Errorf("invalid --processed-mail-ttl: %w", err)
		}

		var totalReaped, totalPurged, totalMailPurged, totalClosed, totalOpen int
		var totalHookedMailClosed int
		var totalOpenMailClosed int
		var totalProcessedMailClosed int
		var totalWispFlushed int

		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Printf("skip invalid db: %s\n", dbName)
				continue
			}

			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 30*time.Second, 30*time.Second)
			if err != nil {
				fmt.Printf("%s: connect error: %v\n", dbName, err)
				continue
			}

			if ok, err := reaper.HasReaperSchema(db); err != nil {
				fmt.Printf("%s: schema check error: %v\n", dbName, err)
				db.Close()
				continue
			} else if !ok {
				fmt.Printf("%s: skipped (no reaper schema)\n", dbName)
				db.Close()
				continue
			}

			// Scan
			scanResult, err := reaper.Scan(db, dbName, maxAge, purgeAge, mailAge, staleAge)
			if err != nil {
				fmt.Printf("%s: scan error: %v\n", dbName, err)
				db.Close()
				continue
			}
			for _, a := range scanResult.Anomalies {
				fmt.Printf("%s: %s %s\n", dbName, style.Warning.Render("ANOMALY:"), a.Message)
			}

			// Reap
			reapResult, err := reaper.Reap(db, dbName, maxAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap error: %v\n", dbName, err)
			} else {
				totalReaped += reapResult.Reaped
				totalOpen += reapResult.OpenRemain
			}

			// Purge
			purgeResult, err := reaper.Purge(db, dbName, purgeAge, mailAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: purge error: %v\n", dbName, err)
			} else {
				totalPurged += purgeResult.WispsPurged
				totalMailPurged += purgeResult.MailPurged
			}

			// Flush dolt_ignored wisp working set to HEAD (gu-tqtwt). Bounds the
			// backlog that otherwise deadlocks bd's pre-migration guard.
			flushResult, err := reaper.FlushWispWorkingSet(db, dbName, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: wisp flush error: %v\n", dbName, err)
			} else {
				totalWispFlushed += flushResult.Flushed
				if flushResult.Flushed > 0 {
					fmt.Printf("  %s: flushed %d pending wisp row change(s) across %v\n",
						dbName, flushResult.Flushed, flushResult.Tables)
				}
				for _, a := range flushResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}

			// Auto-close
			closeResult, err := reaper.AutoClose(db, dbName, staleAge, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: auto-close error: %v\n", dbName, err)
			} else {
				for _, entry := range closeResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd stale, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalClosed += closeResult.Closed
			}

			// Reap hooked mail (gu-hhqk: dead-letter HANDOFF/mail beads past TTL)
			hookedMailResult, err := reaper.ReapHookedMail(db, dbName, hookedMailTTL, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap-hooked-mail error: %v\n", dbName, err)
			} else {
				for _, entry := range hookedMailResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd hooked, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalHookedMailClosed += hookedMailResult.Closed
				for _, a := range hookedMailResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}

			// Reap open mail (gu-ckly: stale P1 coordination mail beads past TTL)
			openMailResult, err := reaper.ReapOpenMail(db, dbName, openMailTTL, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap-open-mail error: %v\n", dbName, err)
			} else {
				for _, entry := range openMailResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd open, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalOpenMailClosed += openMailResult.Closed
				for _, a := range openMailResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}

			// Reap processed mail (gu-ctspx: read/acked message & escalation
			// beads that ack/mark-read never closed, past a short audit TTL)
			processedMailResult, err := reaper.ReapProcessedMail(db, dbName, processedMailTTL, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap-processed-mail error: %v\n", dbName, err)
			} else {
				for _, entry := range processedMailResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd processed, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalProcessedMailClosed += processedMailResult.Closed
				for _, a := range processedMailResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}

			// Reap processed WISP mail (gu-2md8k: the same read/acked
			// message & escalation beads also accumulate in the dolt-ignored
			// wisps table, which is what the open-wisp alert counts; the
			// issues-only sweep above never drains them)
			processedWispMailResult, err := reaper.ReapProcessedWispMail(db, dbName, processedMailTTL, reaperDryRun)
			if err != nil {
				fmt.Printf("%s: reap-processed-wisp-mail error: %v\n", dbName, err)
			} else {
				for _, entry := range processedWispMailResult.ClosedEntries {
					fmt.Printf("  %s %s (%dd processed wisp, db:%s)\n",
						entry.ID, entry.Title, entry.AgeDays, entry.Database)
				}
				totalProcessedMailClosed += processedWispMailResult.Closed
				for _, a := range processedWispMailResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}

			db.Close()
		}

		// Post-merge orphan reconcile (gu-7igu8): complete interrupted refinery
		// reconciles by closing source issues whose merged MR left them stranded
		// non-terminal. Runs BEFORE the active_mr scrub so the source is terminal
		// in time for the same-cycle scrub to clear the dangling active_mr.
		// Operates on the town database only. Best-effort: failures are logged
		// but do not abort the cycle.
		var totalReconScanned, totalReconReconciled, totalReconPreservedWIP int
		if townRoot, twErr := workspace.FindFromCwd(); twErr != nil {
			fmt.Printf("reconcile-orphans: skipped (no town root): %v\n", twErr)
		} else {
			bd := beads.New(townRoot).ForAgentBead()
			reconResult, err := reaper.ReconcileMergedOrphans(bd, reaperDryRun)
			if err != nil {
				fmt.Printf("reconcile-orphans: error: %v\n", err)
			} else {
				totalReconScanned = reconResult.Scanned
				totalReconReconciled = reconResult.Reconciled
				totalReconPreservedWIP = reconResult.PreservedWIP
				for _, entry := range reconResult.ReconciledEntries {
					fmt.Printf("  %s active_mr=%s source=%s closed\n",
						entry.AgentBeadID, entry.ActiveMR, entry.SourceIssue)
				}
				for _, a := range reconResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}
		}

		// Active-MR scrub (gu-dhqm): clear stale active_mr refs on agent beads
		// after the wisp/mail sweeps so any references to wisps just reaped or
		// purged this cycle are caught immediately. Operates on the town
		// database only — agent beads do not live in rig DBs. Best-effort:
		// failures are logged but do not abort the cycle.
		var totalScrubScanned, totalScrubCleared, totalScrubPreservedWIP, totalScrubStillPending int
		if townRoot, twErr := workspace.FindFromCwd(); twErr != nil {
			fmt.Printf("scrub-active-mr: skipped (no town root): %v\n", twErr)
		} else {
			bd := beads.New(townRoot).ForAgentBead()
			scrubResult, err := reaper.ScrubStaleActiveMR(bd, reaperDryRun)
			if err != nil {
				fmt.Printf("scrub-active-mr: error: %v\n", err)
			} else {
				totalScrubScanned = scrubResult.Scanned
				totalScrubCleared = scrubResult.Cleared
				totalScrubPreservedWIP = scrubResult.PreservedWIP
				totalScrubStillPending = scrubResult.StillPending
				for _, entry := range scrubResult.ClearedEntries {
					fmt.Printf("  %s active_mr=%s mr_status=%s source=%s\n",
						entry.AgentBeadID, entry.ActiveMR, entry.MRStatus, entry.SourceIssue)
				}
				for _, a := range scrubResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}
		}

		// Dangling-FK scrub (gu-96uxo): clear mr_id/hook_bead refs on agent
		// beads whose referent wisp was reaped or purged this cycle (now
		// missing). Complements the active_mr scrub above. Operates on the
		// town database only. Best-effort: failures are logged, not fatal.
		var totalFKScanned, totalFKClearedMRID, totalFKClearedHook, totalFKPreservedWIP int
		if townRoot, twErr := workspace.FindFromCwd(); twErr != nil {
			fmt.Printf("scrub-dangling-fk: skipped (no town root): %v\n", twErr)
		} else {
			bd := beads.New(townRoot).ForAgentBead()
			fkResult, err := reaper.ScrubDanglingFKRefs(bd, reaperDryRun)
			if err != nil {
				fmt.Printf("scrub-dangling-fk: error: %v\n", err)
			} else {
				totalFKScanned = fkResult.Scanned
				totalFKClearedMRID = fkResult.ClearedMRID
				totalFKClearedHook = fkResult.ClearedHookBead
				totalFKPreservedWIP = fkResult.PreservedWIP
				for _, entry := range fkResult.ClearedEntries {
					fmt.Printf("  %s %s=%s (referent missing)\n",
						entry.AgentBeadID, entry.Field, entry.Referent)
				}
				for _, a := range fkResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}
		}

		// Git-evidence orphan reconcile (gu-hrweu): the durable-artifact fallback
		// to the agent-bead reconcile above. Runs LAST — after purge has deleted
		// MR wisps and after scrub-active-mr has cleared active_mr refs — so it
		// catches exactly the survivors the agent-bead pass can no longer prove:
		// non-terminal source issues still carrying awaiting_refinery_merge whose
		// merge is provable only by a citing commit on the target branch. Closes
		// across all rig DBs via prefix routing. Best-effort: failures logged.
		var totalGitReconScanned, totalGitReconReconciled, totalGitReconNotMerged, totalGitReconUnverified int
		if townRoot, twErr := workspace.FindFromCwd(); twErr != nil {
			fmt.Printf("reconcile-orphans-git: skipped (no town root): %v\n", twErr)
		} else {
			adapter := newOrphanGitReconcileAdapter(townRoot)
			gitReconResult, err := reaper.ReconcileMergedOrphansByGitEvidence(adapter, adapter, reaperDryRun)
			if err != nil {
				fmt.Printf("reconcile-orphans-git: error: %v\n", err)
			} else {
				totalGitReconScanned = gitReconResult.Scanned
				totalGitReconReconciled = gitReconResult.Reconciled
				totalGitReconNotMerged = gitReconResult.NotYetMerged
				totalGitReconUnverified = gitReconResult.Unverified
				for _, entry := range gitReconResult.ReconciledEntries {
					fmt.Printf("  %s closed (git evidence: citing commit on target branch)\n", entry.SourceIssue)
				}
				for _, a := range gitReconResult.Anomalies {
					fmt.Printf("  %s %s\n", style.Warning.Render("ANOMALY:"), a.Message)
				}
			}
		}

		// Report
		prefix := ""
		if reaperDryRun {
			prefix = "[DRY RUN] "
		}
		fmt.Printf("\n%sReaper cycle complete:\n", prefix)
		fmt.Printf("  Databases:        %d\n", len(databases))
		fmt.Printf("  Reaped:           %d\n", totalReaped)
		fmt.Printf("  Purged:           %d wisps, %d mail\n", totalPurged, totalMailPurged)
		fmt.Printf("  Wisp flushed:     %d pending row change(s)\n", totalWispFlushed)
		fmt.Printf("  Closed:           %d stale issues\n", totalClosed)
		fmt.Printf("  Hooked-mail TTL:  %d ttl-expired\n", totalHookedMailClosed)
		fmt.Printf("  Open-mail TTL:    %d ttl-expired\n", totalOpenMailClosed)
		fmt.Printf("  Processed-mail:   %d closed\n", totalProcessedMailClosed)
		fmt.Printf("  orphan reconcile: scanned=%d reconciled=%d preserved_wip=%d\n",
			totalReconScanned, totalReconReconciled, totalReconPreservedWIP)
		fmt.Printf("  active_mr scrub:  scanned=%d cleared=%d preserved_wip=%d still_pending=%d\n",
			totalScrubScanned, totalScrubCleared, totalScrubPreservedWIP, totalScrubStillPending)
		fmt.Printf("  dangling_fk scrub: scanned=%d cleared_mr_id=%d cleared_hook_bead=%d preserved_wip=%d\n",
			totalFKScanned, totalFKClearedMRID, totalFKClearedHook, totalFKPreservedWIP)
		fmt.Printf("  orphan reconcile (git): scanned=%d reconciled=%d not_yet_merged=%d unverified=%d\n",
			totalGitReconScanned, totalGitReconReconciled, totalGitReconNotMerged, totalGitReconUnverified)
		fmt.Printf("  Open:             %d wisps remain\n", totalOpen)

		return nil
	},
}

var reaperAlertThreshold int

var reaperAlertOpenWispsCmd = &cobra.Command{
	Use:   "alert-open-wisps",
	Short: "Escalate (deduped) when open-wisp count exceeds the alert threshold",
	Long: `Count open wisps across all databases and, if the total exceeds the
alert threshold, raise a deduplicated escalation to the Mayor.

This replaces the old freeform escalate in the mol-dog-reaper formula, which
created a fresh escalation bead every cycle because each cycle's slightly-
different count produced a new signature (gu-ka8aj). The escalation here uses a
stable signature built from the threshold-breach BAND, not the exact count, so
normal workload drift within one band collapses to a single escalation:

  - band 1 ([1x,2x) threshold): medium severity, 4h cooldown
  - band 2+ (>=2x threshold):   high severity,   1h cooldown

Cooldown is applied as the --dedup-window so the escalation does not re-fire
immediately after a previous one closes. Use --dry-run to print the escalation
that would be raised without raising it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// threshold defaults to DefaultAlertThreshold via the flag; threshold <= 0
		// is honored as "alerting disabled" by EvaluateOpenWispAlert (operator opt-out).
		threshold := reaperAlertThreshold
		if threshold <= 0 {
			fmt.Println("open-wisp alerting disabled (threshold <= 0)")
			return nil
		}

		// The escalation gates on the ACTIONABLE count — open wisps past their
		// per-type TTL, i.e. the subset compaction would act on — not the raw
		// open count, which includes within-TTL accumulation that drains
		// naturally and is non-actionable (gu-9ks4i). Resolve the same TTL
		// policy compaction uses so the two counts share one definition.
		workDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}
		townRoot := beads.FindTownRoot(workDir)
		ttls := loadTTLConfig(townRoot, os.Getenv("GT_RIG"))

		databases := reaperDatabaseNames()
		var totalOpen, totalActionable int
		for i, dbName := range databases {
			if err := waitBeforeReaperDatabase(i); err != nil {
				return err
			}
			if err := reaper.ValidateDBName(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "skip invalid db: %s\n", dbName)
				continue
			}
			db, err := reaper.OpenDB(reaperHost, reaperPort, dbName, 10*time.Second, 10*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: connect error: %v\n", dbName, err)
				continue
			}
			open, err := reaper.CountOpenWisps(db)
			if err != nil {
				db.Close()
				fmt.Fprintf(os.Stderr, "%s: count error: %v\n", dbName, err)
				continue
			}
			actionable, err := reaper.CountActionableOpenWisps(db, ttls)
			db.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: actionable count error: %v\n", dbName, err)
				continue
			}
			totalOpen += open
			totalActionable += actionable
		}

		alert := reaper.EvaluateOpenWispAlert(totalActionable, threshold)
		if !alert.Fire {
			fmt.Printf("actionable (past-TTL) open wisps within threshold: %d <= %d (total open: %d; no escalation)\n",
				totalActionable, threshold, totalOpen)
			return nil
		}

		escalateArgs := alert.EscalateArgs(totalActionable, totalOpen, threshold)
		if reaperDryRun {
			fmt.Printf("[DRY RUN] would escalate (%s, band %d, cooldown %s): %d actionable (past-TTL) open wisps exceed %d (total open: %d)\n",
				alert.Severity, alert.Bucket, alert.Cooldown, totalActionable, threshold, totalOpen)
			fmt.Printf("[DRY RUN] gt %s\n", strings.Join(escalateArgs, " "))
			return nil
		}

		gtPath, err := os.Executable()
		if err != nil || gtPath == "" {
			gtPath = "gt"
		}
		escalateCmd := exec.Command(gtPath, escalateArgs...) //nolint:gosec // G204: args constructed internally from validated alert metadata
		escalateCmd.Stdout = os.Stdout
		escalateCmd.Stderr = os.Stderr
		if err := escalateCmd.Run(); err != nil {
			return fmt.Errorf("raise open-wisp escalation: %w", err)
		}
		return nil
	},
}

func init() {
	// Shared flags
	// GH#2601: Default host/port from env vars for non-localhost setups.
	defaultHost := "127.0.0.1"
	if h := os.Getenv("GT_DOLT_HOST"); h != "" {
		defaultHost = h
	} else if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		defaultHost = h
	}
	defaultPort := 3307
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			defaultPort = v
		}
	} else if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			defaultPort = v
		}
	}

	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperRunCmd, reaperDatabasesCmd, reaperReapHookedMailCmd, reaperReapOpenMailCmd, reaperReapProcessedMailCmd, reaperClosePluginReceiptsCmd, reaperFlushWispsCmd, reaperScrubActiveMRCmd, reaperReconcileOrphansCmd, reaperReconcileOrphansGitCmd, reaperScrubDanglingFKCmd, reaperAlertOpenWispsCmd} {
		cmd.Flags().StringVar(&reaperDB, "db", "", "Database name (required for single-db commands)")
		cmd.Flags().StringVar(&reaperHost, "host", defaultHost, "Dolt server host (env: GT_DOLT_HOST)")
		cmd.Flags().IntVar(&reaperPort, "port", defaultPort, "Dolt server port (env: GT_DOLT_PORT)")
		cmd.Flags().BoolVar(&reaperDryRun, "dry-run", false, "Report what would happen without acting")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperRunCmd, reaperAlertOpenWispsCmd} {
		cmd.Flags().StringVar(&reaperDBDelay, "db-delay", "250ms", "Delay between databases to reduce Dolt load")
	}

	// Open-wisp alert threshold flag (gu-ka8aj). Defaults to DefaultAlertThreshold.
	reaperAlertOpenWispsCmd.Flags().IntVar(&reaperAlertThreshold, "threshold", reaper.DefaultAlertThreshold,
		"Open-wisp count above which to escalate")

	// JSON output flag for single-db commands
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperDatabasesCmd, reaperReapHookedMailCmd, reaperReapOpenMailCmd, reaperReapProcessedMailCmd, reaperClosePluginReceiptsCmd, reaperFlushWispsCmd, reaperScrubActiveMRCmd, reaperReconcileOrphansCmd, reaperReconcileOrphansGitCmd, reaperScrubDanglingFKCmd} {
		cmd.Flags().BoolVar(&reaperJSON, "json", false, "Output as JSON")
	}

	reaperClosePluginReceiptsCmd.Flags().StringVar(&reaperPluginReceiptAge, "max-age", "1h", "Max plugin-receipt age before closing")

	// Threshold flags
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperReapCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperMaxAge, "max-age", "24h", "Max wisp age before reaping")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperPurgeCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperPurgeAge, "purge-age", "168h", "Max closed wisp age before purging (7d)")
		cmd.Flags().StringVar(&reaperMailAge, "mail-age", "168h", "Max closed mail age before purging (7d)")
	}
	for _, cmd := range []*cobra.Command{reaperScanCmd, reaperAutoCloseCmd, reaperRunCmd} {
		cmd.Flags().StringVar(&reaperStaleAge, "stale-age", "720h", "Max issue staleness before auto-close (30d)")
	}

	// Hooked-mail TTL flag (GUPP: gu-hhqk). Default aligns with DefaultHookedMailTTL.
	reaperReapHookedMailCmd.Flags().StringVar(&reaperHookedMailTTL, "ttl", reaper.DefaultHookedMailTTL.String(), "Max hooked-mail age before closing as ttl-expired")
	reaperRunCmd.Flags().StringVar(&reaperHookedMailTTL, "hooked-mail-ttl", reaper.DefaultHookedMailTTL.String(), "Max hooked-mail age before closing as ttl-expired")

	// Open-mail TTL flag (gu-ckly). Default aligns with DefaultOpenMailTTL.
	reaperReapOpenMailCmd.Flags().StringVar(&reaperOpenMailTTL, "ttl", reaper.DefaultOpenMailTTL.String(), "Max open-mail age before closing as ttl-expired")
	reaperRunCmd.Flags().StringVar(&reaperOpenMailTTL, "open-mail-ttl", reaper.DefaultOpenMailTTL.String(), "Max open-mail age before closing as ttl-expired")

	// Processed-mail TTL flag (gu-ctspx). Default aligns with DefaultProcessedMailTTL.
	reaperReapProcessedMailCmd.Flags().StringVar(&reaperProcessedMailTTL, "ttl", reaper.DefaultProcessedMailTTL.String(), "Max processed (read/acked) mail age before closing")
	reaperRunCmd.Flags().StringVar(&reaperProcessedMailTTL, "processed-mail-ttl", reaper.DefaultProcessedMailTTL.String(), "Max processed (read/acked) mail age before closing")

	reaperCmd.AddCommand(reaperDatabasesCmd)
	reaperCmd.AddCommand(reaperScanCmd)
	reaperCmd.AddCommand(reaperReapCmd)
	reaperCmd.AddCommand(reaperPurgeCmd)
	reaperCmd.AddCommand(reaperAutoCloseCmd)
	reaperCmd.AddCommand(reaperReapHookedMailCmd)
	reaperCmd.AddCommand(reaperReapOpenMailCmd)
	reaperCmd.AddCommand(reaperReapProcessedMailCmd)
	reaperCmd.AddCommand(reaperClosePluginReceiptsCmd)
	reaperCmd.AddCommand(reaperFlushWispsCmd)
	reaperCmd.AddCommand(reaperScrubActiveMRCmd)
	reaperCmd.AddCommand(reaperReconcileOrphansCmd)
	reaperCmd.AddCommand(reaperReconcileOrphansGitCmd)
	reaperCmd.AddCommand(reaperScrubDanglingFKCmd)
	reaperCmd.AddCommand(reaperRunCmd)
	reaperCmd.AddCommand(reaperAlertOpenWispsCmd)

	rootCmd.AddCommand(reaperCmd)
}
