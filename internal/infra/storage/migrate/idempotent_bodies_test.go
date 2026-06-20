// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// OP #214: every migration body must be idempotent, so a partial-apply
// followed by a retry recovers cleanly. The runner wraps non-concurrent
// migrations in a transaction with the version-INSERT, but @CONCURRENT
// migrations and any future hand-tuned step still need this guarantee
// at the SQL level. The cheapest way to enforce it without standing up
// a Postgres for every unit run is to make each migration prove it
// uses idempotency markers on every mutation it performs:
//
//   - CREATE TABLE      → IF NOT EXISTS
//   - CREATE INDEX      → IF NOT EXISTS
//   - ADD COLUMN        → IF NOT EXISTS
//   - DROP COLUMN       → IF EXISTS
//   - DROP INDEX        → IF EXISTS
//   - DROP CONSTRAINT   → IF EXISTS
//   - DROP TABLE        → IF EXISTS
//   - ADD CONSTRAINT    → preceded by DROP CONSTRAINT IF EXISTS
//                         (or guarded by an information_schema check)
//   - INSERT INTO schema_migrations → ON CONFLICT (version) DO NOTHING
//
// Comments and string-literal contents are stripped before scanning so
// a comment that mentions "CREATE TABLE foo" does not trip the checker.
//
// The version-INSERT must also be the last executable statement in
// every migration body so a partial apply leaves the version row
// absent, and the runner re-runs the whole body on the next pod.

var (
	// Strip /* ... */ block comments and -- ... line comments before
	// scanning. Single-quoted string literals are also nulled out so a
	// format() call that contains "create table foo" does not match.
	reBlockComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment   = regexp.MustCompile(`--[^\n]*`)
	reSQLStringLit  = regexp.MustCompile(`'([^']|'')*'`)
	reCreateTable   = regexp.MustCompile(`(?i)\bcreate\s+table\b(?:\s+if\s+not\s+exists\b)?`)
	reCreateIndex   = regexp.MustCompile(`(?i)\bcreate\s+(?:unique\s+)?index\b(?:\s+if\s+not\s+exists\b)?`)
	reAddColumn     = regexp.MustCompile(`(?i)\badd\s+column\b(?:\s+if\s+not\s+exists\b)?`)
	reDropColumn    = regexp.MustCompile(`(?i)\bdrop\s+column\b(?:\s+if\s+exists\b)?`)
	reDropIndex     = regexp.MustCompile(`(?i)\bdrop\s+index\b(?:\s+if\s+exists\b)?`)
	reDropTable     = regexp.MustCompile(`(?i)\bdrop\s+table\b(?:\s+if\s+exists\b)?`)
	reDropConstr    = regexp.MustCompile(`(?i)\bdrop\s+constraint\b(?:\s+if\s+exists\b)?`)
	reAddConstraint = regexp.MustCompile(`(?i)\badd\s+constraint\b\s+(\w+)`)
	reInsertSM      = regexp.MustCompile(`(?is)\binsert\s+into\s+schema_migrations\b.*?\bvalues\b[^;]*;`)
	reOnConflict    = regexp.MustCompile(`(?i)\bon\s+conflict\s*\(\s*version\s*\)\s*do\s+nothing\b`)
)

// stripCommentsAndStrings returns the migration body with -- line
// comments, /* */ block comments, and the contents of single-quoted
// strings replaced by spaces of the same length. Length is preserved
// so the regexes operate on the original byte offsets.
func stripCommentsAndStrings(body string) string {
	body = reBlockComment.ReplaceAllStringFunc(body, blank)
	body = reLineComment.ReplaceAllStringFunc(body, blank)
	body = reSQLStringLit.ReplaceAllStringFunc(body, blank)
	return body
}

func blank(s string) string { return strings.Repeat(" ", len(s)) }

func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}

	type guard struct {
		re      *regexp.Regexp
		marker  string
		ddl     string
		require func(string) bool
	}
	guards := []guard{
		{re: reCreateTable, marker: "if not exists", ddl: "CREATE TABLE"},
		{re: reCreateIndex, marker: "if not exists", ddl: "CREATE INDEX"},
		{re: reAddColumn, marker: "if not exists", ddl: "ADD COLUMN"},
		{re: reDropColumn, marker: "if exists", ddl: "DROP COLUMN"},
		{re: reDropIndex, marker: "if exists", ddl: "DROP INDEX"},
		{re: reDropTable, marker: "if exists", ddl: "DROP TABLE"},
		{re: reDropConstr, marker: "if exists", ddl: "DROP CONSTRAINT"},
	}

	for _, m := range migs {
		m := m
		t.Run(m.Filename, func(t *testing.T) {
			t.Parallel()
			body := stripCommentsAndStrings(m.Body)
			lower := strings.ToLower(body)

			for _, g := range guards {
				for _, match := range g.re.FindAllString(lower, -1) {
					if !strings.Contains(strings.ToLower(match), g.marker) {
						t.Errorf("%s: %s without %s — not idempotent against partial apply: %q",
							m.Filename, g.ddl, strings.ToUpper(g.marker), match)
					}
				}
			}

			// Each ADD CONSTRAINT must be preceded somewhere in the
			// body by DROP CONSTRAINT IF EXISTS for the same name (or
			// the body must be a fresh CREATE TABLE that names the
			// constraint inline — which the CREATE TABLE IF NOT EXISTS
			// check above already covers).
			for _, sm := range reAddConstraint.FindAllStringSubmatch(lower, -1) {
				name := sm[1]
				dropPattern := regexp.MustCompile(
					`(?i)drop\s+constraint\s+if\s+exists\s+` + regexp.QuoteMeta(name) + `\b`)
				if !dropPattern.MatchString(lower) {
					t.Errorf("%s: ADD CONSTRAINT %s without preceding DROP CONSTRAINT IF EXISTS %s — second apply will fail with duplicate-object",
						m.Filename, name, name)
				}
			}

			// The schema_migrations INSERT must use ON CONFLICT
			// (version) DO NOTHING so a row that was partially written
			// by a prior pod (or a backfill tool) doesn't fail the
			// retry.
			if loc := reInsertSM.FindStringIndex(lower); loc != nil {
				stmt := lower[loc[0]:loc[1]]
				if !reOnConflict.MatchString(stmt) {
					t.Errorf("%s: INSERT INTO schema_migrations without ON CONFLICT (version) DO NOTHING — partial apply will fail on retry: %q",
						m.Filename, stmt)
				}
			}
		})
	}
}

// TestMigrationsEndWithVersionInsert pins the per-migration sentinel:
// the very last executable statement of every migration body must be
// the INSERT INTO schema_migrations(version) for that migration's own
// version. If any other DDL trails the version-INSERT, a crash between
// the two leaves the version row recorded but the trailing DDL un-run
// — exactly the failure mode #214 forbids.
//
// Migrations whose runner-coupled record path inserts the version
// (every migration except 0001 which establishes the table) are
// allowed to omit the body-level INSERT entirely; the runner wraps
// the whole body in a transaction with recordAppliedTx so the
// in-tx INSERT is the implicit last statement. The sentinel test
// therefore only requires: IF a body contains an INSERT INTO
// schema_migrations, that INSERT must be the final non-comment,
// non-whitespace statement.
func TestMigrationsEndWithVersionInsert(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}

	for _, m := range migs {
		m := m
		t.Run(m.Filename, func(t *testing.T) {
			t.Parallel()
			body := stripCommentsAndStrings(m.Body)
			lowered := strings.ToLower(body)

			loc := reInsertSM.FindStringIndex(lowered)
			if loc == nil {
				// No body-level INSERT. The runner records the
				// version on its own; nothing to pin here.
				return
			}
			trailing := strings.TrimSpace(body[loc[1]:])
			// Permit trailing whitespace and trailing semicolons but
			// no further executable content.
			trailing = strings.Trim(trailing, "; \t\r\n")
			if trailing != "" {
				t.Errorf("%s: trailing executable statements after INSERT INTO schema_migrations — version row would be recorded before %q runs:\n%s",
					m.Filename, "tail", trailing)
			}
		})
	}
}
