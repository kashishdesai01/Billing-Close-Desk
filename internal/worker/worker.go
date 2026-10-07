// Package worker delivers versioned immutable documents with fenced local claims.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"billingdesk/internal/billing"
	"billingdesk/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Worker struct {
	DB  *pgxpool.Pool
	ERP billing.ERP
}
type Claim struct {
	ID, Token, Month string
	Attempts         int
	Document         billing.Document
}

func (w *Worker) Claim(ctx context.Context) (*Claim, error) {
	c := &Claim{Token: store.ID()}
	var raw []byte
	// Dependencies require a confirmed original plus receiver readiness (Put checks existence).
	e := w.DB.QueryRow(ctx, `UPDATE outbox SET claim_token=$1,locked_until=now()+interval '15 seconds',attempts=attempts+1 WHERE id=(SELECT o.id FROM outbox o WHERE o.status='pending' AND o.next_at<=now() AND (o.locked_until IS NULL OR o.locked_until<now()) AND (o.document_type<>'credit' OR EXISTS(SELECT 1 FROM outbox p WHERE p.document_id=o.invoice_id AND p.sequence=1 AND p.status='confirmed')) AND NOT EXISTS(SELECT 1 FROM outbox p WHERE p.document_id=o.document_id AND p.sequence<o.sequence AND p.status<>'confirmed') ORDER BY o.created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,to_char(month,'YYYY-MM-DD'),attempts,payload`, c.Token).Scan(&c.ID, &c.Month, &c.Attempts, &raw)
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal(raw, &c.Document)
	return c, e
}
func (w *Worker) Finish(ctx context.Context, c *Claim, delivery error) (bool, error) {
	tx, e := w.DB.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Same lock order as commands. Completion is not a financial posting and may drain after close.
	if _, e = tx.Exec(ctx, `SELECT month FROM periods WHERE month=$1 FOR UPDATE`, c.Month); e != nil {
		return false, e
	}
	status, message := "confirmed", ""
	delay := 0
	if delivery != nil {
		status = "pending"
		message = "receiver unavailable or rejected document"
		delay = 1 << min(c.Attempts, 8)
		if c.Attempts >= 8 {
			status = "dead"
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE outbox SET status=$3,last_error=$4,next_at=now()+make_interval(secs=>$5),locked_until=NULL,claim_token=NULL WHERE id=$1 AND claim_token=$2 AND locked_until>now() AND status='pending'`, c.ID, c.Token, status, message, delay)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, e = tx.Exec(ctx, `UPDATE periods SET generation=generation+1 WHERE month=$1`, c.Month); e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit(actor,action,entity,details) VALUES('worker','erp_delivery',$1,$2)`, c.ID, store.JSON(map[string]any{"status": status, "attempt": c.Attempts})); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
func (w *Worker) Tick(ctx context.Context) (int, error) {
	count := 0
	for range 10 {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		c, e := w.Claim(ctx)
		if e != nil {
			if e.Error() == "no rows in result set" {
				return count, nil
			}
			return count, e
		}
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		e = w.ERP.Put(sendCtx, c.Document)
		cancel()
		ok, finishErr := w.Finish(ctx, c, e)
		if finishErr != nil {
			return count, fmt.Errorf("finish claim: %w", finishErr)
		}
		if ok {
			count++
		}
	}
	return count, nil
}
