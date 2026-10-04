package billing

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

func (s *Service) State(ctx context.Context, month string) (map[string]any, error) {
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := map[string]string{
		"periods":    `SELECT month::text,state,generation,closed_at,close_run_id FROM periods ORDER BY month DESC`,
		"customers":  `SELECT c.*,k.rate::text,u.impressions,u.revision AS usage_revision FROM customers c JOIN LATERAL (SELECT * FROM contracts WHERE customer_id=c.id ORDER BY version DESC LIMIT 1) k ON true JOIN usage_facts u ON u.customer_id=c.id WHERE u.month=$1 ORDER BY c.name`,
		"invoices":   `SELECT i.id,i.customer_id,i.contract_id,i.month::text AS period,i.state,i.kind,i.revision,i.version,i.impressions,i.rate::text AS rate,i.total_cents,i.credited_cents,i.number,i.snapshot,i.created_by,i.snapshot->>'customer_name' AS customer_name FROM invoices i JOIN customers c ON c.id=i.customer_id WHERE i.month=$1 ORDER BY i.number NULLS FIRST,i.id`,
		"approvals":  `SELECT a.*,coalesce(i.customer_id,ci.customer_id) AS customer_id,coalesce(i.total_cents,cr.amount_cents) AS amount_cents,coalesce(i.snapshot,ci.snapshot) AS invoice_snapshot,i.number AS invoice_number,coalesce(i.id,ci.id) AS invoice_id,coalesce(i.revision,ci.revision) AS invoice_revision,coalesce(i.snapshot,ci.snapshot)->>'customer_name' AS customer_name,cr.reason AS credit_reason,ci.number AS linked_invoice_number,(SELECT count(*) FROM decisions d WHERE d.approval_id=a.id AND accept) AS votes,coalesce((SELECT jsonb_agg(actor) FROM decisions d WHERE d.approval_id=a.id),'[]') AS voters FROM approvals a LEFT JOIN invoices i ON a.document_type='invoice' AND a.document_id=i.id LEFT JOIN credit_requests cr ON a.document_type='credit' AND a.document_id=cr.id LEFT JOIN invoices ci ON ci.id=cr.invoice_id WHERE coalesce(i.month,cr.month)=$1 AND ((a.document_type='invoice' AND i.state='pending' AND a.revision=i.revision) OR (a.document_type='credit' AND cr.state='pending')) ORDER BY a.id`,
		"credits":    `SELECT cr.*,i.customer_id FROM credit_requests cr JOIN invoices i ON i.id=cr.invoice_id WHERE cr.month=$1 ORDER BY cr.id`,
		"outbox":     `SELECT id,document_id,document_type,sequence,status,attempts,last_error,created_at FROM outbox WHERE month=$1 ORDER BY created_at DESC`,
		"breaks":     `SELECT * FROM breaks WHERE month=$1 ORDER BY state,kind,subject`,
		"recon_runs": `SELECT * FROM recon_runs WHERE month=$1 ORDER BY id DESC LIMIT 10`,
		"audit":      `SELECT id,actor,action,entity,at FROM audit ORDER BY id DESC LIMIT 40`,
		"ledger":     `SELECT e.id,e.kind,e.business_key,e.created_at,l.account,l.direction,l.amount_cents,c.name FROM journal_entries e JOIN journal_lines l ON l.entry_id=e.id JOIN customers c ON c.id=l.customer_id WHERE e.month=$1 ORDER BY e.created_at DESC,l.id LIMIT 100`,
	}
	result := map[string]any{}
	for name, q := range queries {
		var raw []byte
		wrapped := `SELECT coalesce(jsonb_agg(to_jsonb(x)),'[]') FROM (` + q + `) x`
		if name == "audit" || name == "periods" {
			e = tx.QueryRow(ctx, wrapped).Scan(&raw)
		} else {
			e = tx.QueryRow(ctx, wrapped, month).Scan(&raw)
		}
		if e != nil {
			return nil, e
		}
		result[name] = json.RawMessage(raw)
	}
	issues, e := verifyTx(ctx, tx)
	if e != nil {
		return nil, e
	}
	result["verification"] = map[string]any{"passed": len(issues) == 0, "issues": issues}
	return result, nil
}
