// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// TestPartitionTriggerUsesNewCreatedAt (OP #215, finding #139) pins
// the partition trigger contract at the SQL-text level: every BEFORE
// INSERT trigger function on a partitioned parent (resource_changes,
// ehr_events) that derives created_month must do so from
// NEW.created_at, never from now()/CURRENT_TIMESTAMP/CURRENT_DATE.
//
// We assert two things:
//
//  1. The CREATE-OR-REPLACE for set_resource_changes_created_month
//     and set_ehr_events_created_month MUST appear in some migration
//     with the body
//     new.created_month := date_trunc('month', new.created_at)::date;
//     and MUST NOT appear with date_trunc('month', now()) anywhere
//     AFTER the corrective migration. Migrations earlier in the
//     sequence are allowed to use now() — that's the bug the fix
//     replaces — so we look at the LAST CREATE OR REPLACE for each
//     function to determine the deployed behavior.
//
//  2. No new migration introduced after the corrective migration may
//     regress the function back to now()-based derivation.
func TestPartitionTriggerUsesNewCreatedAt(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}

	// Find the LAST migration body that defines each function. That's
	// the one whose behavior the database ends up with after a clean
	// apply. Migrations are sorted by version in Embedded().
	type fnState struct {
		body string
		mig  string
	}
	last := map[string]*fnState{
		"set_resource_changes_created_month": nil,
		"set_ehr_events_created_month":       nil,
	}
	for _, m := range migs {
		body := strings.ToLower(stripCommentsAndStrings(m.Body))
		for fn := range last {
			if !strings.Contains(body, "function "+fn) {
				continue
			}
			// Capture the function body — between the `as $$` and the
			// closing `$$;`. Anchored to the function name so two
			// definitions in the same file don't collide.
			pattern := regexp.MustCompile(
				`(?is)create\s+(?:or\s+replace\s+)?function\s+` +
					regexp.QuoteMeta(fn) +
					`\b.*?as\s*\$\$(.*?)\$\$`)
			sm := pattern.FindStringSubmatch(body)
			if sm == nil {
				continue
			}
			last[fn] = &fnState{body: sm[1], mig: m.Filename}
		}
	}

	for fn, st := range last {
		if st == nil {
			t.Errorf("expected a CREATE [OR REPLACE] FUNCTION %s in some migration; not found", fn)
			continue
		}
		if !strings.Contains(st.body, "new.created_at") {
			t.Errorf("trigger function %s (last defined in %s) must derive created_month from NEW.created_at; body did not reference new.created_at: %q",
				fn, st.mig, st.body)
		}
		if regexp.MustCompile(`\bnow\s*\(\s*\)`).MatchString(st.body) {
			t.Errorf("trigger function %s (last defined in %s) must NOT use now() for created_month — that re-stamps backfill rows to the current month, violating the schema invariant. Body: %q",
				fn, st.mig, st.body)
		}
		if regexp.MustCompile(`current_(timestamp|date)\b`).MatchString(st.body) {
			t.Errorf("trigger function %s (last defined in %s) must NOT use CURRENT_TIMESTAMP/CURRENT_DATE for created_month; use NEW.created_at",
				fn, st.mig)
		}
	}
}
