package billing

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Verify uses one repeatable-read snapshot, including all dependent sums.
func (s *Service) Verify(ctx context.Context) ([]string, error) {
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return verifyTx(ctx, tx)
}
func verifyTx(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, e := tx.Query(ctx, `
 SELECT 'journal balance: '||e.id FROM journal_entries e LEFT JOIN journal_lines l ON l.entry_id=e.id GROUP BY e.id HAVING count(l.id)<2 OR sum(CASE WHEN l.direction='D' THEN l.amount_cents ELSE -l.amount_cents END)<>0
 UNION ALL SELECT 'pricing mismatch: '||id FROM invoices WHERE total_cents<>round(impressions::numeric*rate/10)
 UNION ALL SELECT 'credit aggregate: '||i.id FROM invoices i WHERE credited_cents<>coalesce((SELECT sum(amount_cents) FROM credit_memos c WHERE c.invoice_id=i.id),0)
 UNION ALL SELECT 'issue entry: '||i.id FROM invoices i WHERE (i.state IN ('issued','void')) AND ((SELECT count(*) FROM journal_entries j WHERE j.invoice_id=i.id AND j.kind='issue')<>1 OR i.number IS NULL OR i.issued_at IS NULL)
 UNION ALL SELECT 'journal/document link: '||j.id FROM journal_entries j JOIN invoices i ON i.id=j.invoice_id WHERE j.month<>i.month OR EXISTS(SELECT 1 FROM journal_lines l WHERE l.entry_id=j.id AND l.customer_id<>i.customer_id) OR (j.kind='issue' AND (i.state NOT IN ('issued','void') OR j.business_key<>i.id||':issue')) OR (j.kind='void' AND (i.state<>'void' OR j.business_key<>i.id||':void')) OR (j.kind='credit' AND NOT EXISTS(SELECT 1 FROM credit_memos c JOIN credit_requests r ON r.id=c.request_id WHERE j.business_key=r.id||':credit' AND c.invoice_id=i.id AND r.invoice_id=i.id AND r.state='issued' AND c.amount_cents=r.amount_cents))
 UNION ALL SELECT 'void entry: '||i.id FROM invoices i WHERE (i.state='void')<>(EXISTS(SELECT 1 FROM journal_entries j WHERE j.invoice_id=i.id AND j.kind='void'))
 UNION ALL SELECT 'credit entry: '||c.id FROM credit_memos c WHERE NOT EXISTS(SELECT 1 FROM journal_entries e WHERE e.business_key=c.request_id||':credit')
 UNION ALL SELECT 'AR mismatch: '||c.id FROM customers c WHERE coalesce((SELECT sum(CASE WHEN direction='D' THEN amount_cents ELSE -amount_cents END) FROM journal_lines WHERE account='1100' AND customer_id=c.id),0)<>coalesce((SELECT sum(total_cents-credited_cents) FROM invoices WHERE customer_id=c.id AND state='issued'),0)
 UNION ALL SELECT 'number sequence mismatch' WHERE (SELECT count(*) FROM invoices WHERE number IS NOT NULL)<>(SELECT value FROM counters WHERE series='invoice') OR coalesce((SELECT max(number) FROM invoices),0)<>(SELECT value FROM counters WHERE series='invoice')
 UNION ALL SELECT 'credit number sequence mismatch' WHERE (SELECT count(*) FROM credit_memos)<>(SELECT value FROM counters WHERE series='credit') OR coalesce((SELECT max(number) FROM credit_memos),0)<>(SELECT value FROM counters WHERE series='credit')
 UNION ALL SELECT 'posting recipe: '||j.id FROM journal_entries j WHERE (SELECT count(*) FROM journal_lines l WHERE l.entry_id=j.id)<>2 OR EXISTS(SELECT 1 FROM journal_lines l WHERE l.entry_id=j.id AND (l.account,l.direction) NOT IN (SELECT CASE j.kind WHEN 'issue' THEN '1100' WHEN 'credit' THEN '4900' ELSE '4000' END,'D' UNION ALL SELECT CASE j.kind WHEN 'issue' THEN '4000' ELSE '1100' END,'C'))
 UNION ALL SELECT 'posting amount: '||j.id FROM journal_entries j JOIN invoices i ON i.id=j.invoice_id WHERE (SELECT sum(amount_cents) FROM journal_lines WHERE entry_id=j.id AND direction='D') IS DISTINCT FROM CASE WHEN j.kind IN ('issue','void') THEN i.total_cents ELSE (SELECT amount_cents FROM credit_memos c WHERE c.request_id||':credit'=j.business_key) END
 `)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	issues := []string{}
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			return nil, e
		}
		issues = append(issues, v)
	}
	return issues, rows.Err()
}
