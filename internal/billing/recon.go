package billing

import (
	"context"
	"fmt"
	"sort"

	"billingdesk/internal/store"
	"github.com/jackc/pgx/v5"
)

type finding struct{ Kind, Subject, Expected, Actual string }

func (s *Service) reconcile(ctx context.Context, tx pgx.Tx, actor, month string, generation int64) (any, error) {
	// Fetch an entire bounded simulator snapshot. A failed fetch never resolves breaks.
	docs, e := s.ERP.List(ctx)
	if e != nil {
		return nil, fail(502, "ERP snapshot unavailable; no breaks changed")
	}
	actual := map[string]Document{}
	for _, d := range docs {
		if _, exists := actual[d.ID]; exists {
			return nil, conflict("duplicate ERP source key")
		}
		actual[d.ID] = d
	}
	expected := map[string]Document{}
	rows, e := tx.Query(ctx, `SELECT DISTINCT ON(document_id) payload FROM outbox WHERE month=$1 ORDER BY document_id,sequence DESC`, month)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var d Document
		if e = rows.Scan(&d); e != nil {
			rows.Close()
			return nil, e
		}
		expected[d.ID] = d
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	findings := []finding{}
	for id, want := range expected {
		got, ok := actual[id]
		if !ok {
			findings = append(findings, finding{"missing_in_erp", id, fmt.Sprint(want.Amount), "missing"})
		} else if digest(want) != digest(got) {
			findings = append(findings, finding{"erp_mismatch", id, string(store.JSON(want)), string(store.JSON(got))})
		}
	}
	for id, got := range actual {
		if got.Month == month {
			if _, ok := expected[id]; !ok {
				findings = append(findings, finding{"orphan_in_erp", id, "absent", "present"})
			}
		}
	}
	type usageEvidence struct {
		Customer string `json:"customer"`
		Quantity *int64 `json:"quantity"`
		Revision *int64 `json:"revision"`
		Billed   int64  `json:"billed"`
	}
	usageInputs := []usageEvidence{}
	rows, e = tx.Query(ctx, `SELECT coalesce(u.customer_id,b.customer_id),u.impressions,u.revision,coalesce(b.impressions,0) FROM (SELECT customer_id,impressions,revision FROM usage_facts WHERE month=$1) u FULL JOIN (SELECT customer_id,sum(impressions) AS impressions FROM invoices WHERE month=$1 AND state='issued' GROUP BY customer_id) b USING(customer_id) ORDER BY 1`, month)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		var qty, revision *int64
		var billed int64
		if e = rows.Scan(&id, &qty, &revision, &billed); e != nil {
			rows.Close()
			return nil, e
		}
		usageInputs = append(usageInputs, usageEvidence{id, qty, revision, billed})
		if qty == nil {
			findings = append(findings, finding{"billed_without_usage", id, "source record missing", fmt.Sprint(billed)})
		} else if *qty != billed {
			findings = append(findings, finding{"usage_mismatch", id, fmt.Sprint(*qty), fmt.Sprint(billed)})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	// Deterministic evidence ordering and a digest identify precisely this source set.
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Kind+findings[i].Subject < findings[j].Kind+findings[j].Subject
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	expectedInputs := []Document{}
	for _, d := range expected {
		expectedInputs = append(expectedInputs, d)
	}
	sort.Slice(expectedInputs, func(i, j int) bool { return expectedInputs[i].ID < expectedInputs[j].ID })
	evidence := map[string]any{"month": month, "generation": generation, "actual_erp": docs, "expected_documents": expectedInputs, "usage": usageInputs}
	var run int64
	if e = tx.QueryRow(ctx, `INSERT INTO recon_runs(month,generation,source_hash,findings,evidence) VALUES($1,$2,$3,$4,$5) RETURNING id`, month, generation, digest(evidence), len(findings), store.JSON(evidence)).Scan(&run); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE breaks SET state='resolved',run_id=$2 WHERE month=$1 AND state='open'`, month, run); e != nil {
		return nil, e
	}
	for _, f := range findings {
		// Count a recurrence only when the discrepancy was resolved by an earlier run.
		_, e = tx.Exec(ctx, `INSERT INTO breaks(id,month,kind,subject,expected,actual,state,run_id) VALUES($1,$2,$3,$4,$5,$6,'open',$7) ON CONFLICT(month,kind,subject) DO UPDATE SET expected=excluded.expected,actual=excluded.actual,state='open',occurrences=breaks.occurrences+CASE WHEN breaks.run_id<>excluded.run_id AND breaks.state='resolved' THEN 1 ELSE 0 END,run_id=excluded.run_id`, store.ID(), month, f.Kind, f.Subject, f.Expected, f.Actual, run)
		if e != nil {
			return nil, e
		}
	}
	return map[string]any{"run_id": run, "findings": len(findings), "generation": generation}, nil
}
