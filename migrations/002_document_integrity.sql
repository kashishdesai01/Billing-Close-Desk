-- Preserve issued history while allowing deliberate withdrawal of unissued work.
ALTER TABLE invoices DROP CONSTRAINT invoices_state_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_state_check CHECK(state IN ('draft','pending','approved','issued','void','cancelled'));
ALTER TABLE credit_requests DROP CONSTRAINT credit_requests_state_check;
ALTER TABLE credit_requests ADD CONSTRAINT credit_requests_state_check CHECK(state IN ('pending','approved','rejected','issued','cancelled'));
ALTER TABLE invoices DROP CONSTRAINT invoices_source_key_key;
CREATE UNIQUE INDEX one_active_source ON invoices(source_key) WHERE source_key IS NOT NULL AND state NOT IN ('void','cancelled');
DROP INDEX one_original;
CREATE UNIQUE INDEX one_original ON invoices(customer_id,month) WHERE kind='original' AND state NOT IN ('void','cancelled');
CREATE OR REPLACE FUNCTION protect_invoice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.id<>OLD.id THEN RAISE EXCEPTION 'document identity immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state IN ('issued','void') AND (NEW.customer_id,NEW.contract_id,NEW.month,NEW.kind,NEW.source_key,NEW.original_id,NEW.revision,NEW.impressions,NEW.rate,NEW.total_cents,NEW.snapshot,NEW.created_by,NEW.number,NEW.issued_at,NEW.reason,NEW.makers) IS DISTINCT FROM (OLD.customer_id,OLD.contract_id,OLD.month,OLD.kind,OLD.source_key,OLD.original_id,OLD.revision,OLD.impressions,OLD.rate,OLD.total_cents,OLD.snapshot,OLD.created_by,OLD.number,OLD.issued_at,OLD.reason,OLD.makers) THEN RAISE EXCEPTION 'issued content immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state IN ('void','cancelled') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'final document immutable' USING ERRCODE='23514'; END IF;
 IF NEW.state<>OLD.state AND NOT ((OLD.state='draft' AND NEW.state='pending') OR (OLD.state='pending' AND NEW.state IN ('draft','approved')) OR (OLD.state='approved' AND NEW.state IN ('draft','issued')) OR (OLD.state='issued' AND NEW.state='void') OR (OLD.state IN ('draft','pending','approved') AND NEW.state='cancelled')) THEN RAISE EXCEPTION 'invalid transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='issued' AND OLD.state<>'issued' AND (NEW.number IS NULL OR NOT EXISTS(SELECT 1 FROM approvals WHERE document_type='invoice' AND document_id=NEW.id AND revision=NEW.revision AND state='approved')) THEN RAISE EXCEPTION 'approval required' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;

CREATE FUNCTION approval_insert_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state<>'pending' THEN RAISE EXCEPTION 'new approval must start pending' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER approval_insert_guard BEFORE INSERT ON approvals FOR EACH ROW EXECUTE FUNCTION approval_insert_guard();
CREATE FUNCTION outbox_content_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.id,NEW.document_id,NEW.invoice_id,NEW.month,NEW.document_type,NEW.sequence,NEW.payload,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.document_id,OLD.invoice_id,OLD.month,OLD.document_type,OLD.sequence,OLD.payload,OLD.created_at) THEN RAISE EXCEPTION 'outbox content immutable' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER outbox_content_guard BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION outbox_content_guard();
-- Usage is mutable source evidence. Serialize every import with close and
-- invalidate the reconciliation cutoff even when an owner/importer writes it.
CREATE FUNCTION usage_generation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE m date; BEGIN
 IF TG_OP='UPDATE' AND (NEW.customer_id,NEW.month) IS DISTINCT FROM (OLD.customer_id,OLD.month) THEN RAISE EXCEPTION 'usage identity immutable' USING ERRCODE='23514'; END IF;
 m:=CASE WHEN TG_OP='DELETE' THEN OLD.month ELSE NEW.month END;
 PERFORM month FROM periods WHERE month=m FOR UPDATE;
 UPDATE periods SET generation=generation+1 WHERE month=m;
 IF TG_OP='UPDATE' THEN NEW.revision:=greatest(NEW.revision,OLD.revision+1); END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER usage_generation_guard BEFORE INSERT OR UPDATE OR DELETE ON usage_facts FOR EACH ROW EXECUTE FUNCTION usage_generation_guard();
ALTER TABLE recon_runs ADD COLUMN evidence jsonb NOT NULL DEFAULT '{}';
