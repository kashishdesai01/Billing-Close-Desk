CREATE TABLE users(id text PRIMARY KEY, name text NOT NULL, role text NOT NULL CHECK(role IN ('analyst','approver','controller','viewer')), active boolean NOT NULL DEFAULT true);
ALTER TABLE users ADD COLUMN email text UNIQUE;
ALTER TABLE users ADD COLUMN oidc_subject text UNIQUE;
CREATE TABLE periods(month date PRIMARY KEY CHECK(extract(day FROM month)=1), state text NOT NULL DEFAULT 'open' CHECK(state IN ('open','closed')), generation bigint NOT NULL DEFAULT 0, closed_at timestamptz, close_run_id bigint);
CREATE TABLE customers(id text PRIMARY KEY, name text NOT NULL, crm_order_ref text, cap_cents bigint NOT NULL CHECK(cap_cents>0));
CREATE TABLE contracts(id text PRIMARY KEY, customer_id text NOT NULL REFERENCES customers, version int NOT NULL, rate numeric(18,6) NOT NULL CHECK(rate>0), UNIQUE(customer_id,version));
CREATE TABLE usage_facts(customer_id text REFERENCES customers, month date REFERENCES periods, impressions bigint NOT NULL CHECK(impressions>=0 AND impressions<=1000000000000), revision bigint NOT NULL DEFAULT 1, PRIMARY KEY(customer_id,month));
CREATE TABLE invoices(id text PRIMARY KEY, customer_id text NOT NULL REFERENCES customers, contract_id text NOT NULL REFERENCES contracts, month date NOT NULL REFERENCES periods, kind text NOT NULL CHECK(kind IN ('original','supplement')), source_key text UNIQUE, original_id text REFERENCES invoices, state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','pending','approved','issued','void')), revision bigint NOT NULL DEFAULT 1, version bigint NOT NULL DEFAULT 1, impressions bigint NOT NULL CHECK(impressions>=0 AND impressions<=1000000000000), rate numeric(18,6) NOT NULL CHECK(rate>0), total_cents bigint NOT NULL CHECK(total_cents>0 AND total_cents<=1000000000000), credited_cents bigint NOT NULL DEFAULT 0 CHECK(credited_cents>=0 AND credited_cents<=total_cents), snapshot jsonb NOT NULL, created_by text NOT NULL REFERENCES users, number bigint UNIQUE, issued_at timestamptz, reason text NOT NULL DEFAULT '', CHECK((kind='original' AND original_id IS NULL) OR (kind='supplement' AND original_id IS NOT NULL AND source_key IS NOT NULL)));
CREATE UNIQUE INDEX one_original ON invoices(customer_id,month) WHERE kind='original' AND state<>'void';
CREATE TABLE counters(series text PRIMARY KEY, value bigint NOT NULL);
INSERT INTO counters VALUES ('invoice',0),('credit',0);
ALTER TABLE invoices ADD COLUMN makers text[] NOT NULL DEFAULT ARRAY[]::text[];
CREATE TABLE credit_requests(id text PRIMARY KEY, invoice_id text NOT NULL REFERENCES invoices, month date NOT NULL REFERENCES periods, amount_cents bigint NOT NULL CHECK(amount_cents>0), reason text NOT NULL CHECK(length(reason)>=5), state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected','issued')), created_by text NOT NULL REFERENCES users);
CREATE TABLE approvals(id text PRIMARY KEY, document_type text NOT NULL CHECK(document_type IN ('invoice','credit')), document_id text NOT NULL, revision bigint NOT NULL, content_hash text NOT NULL, creator text NOT NULL REFERENCES users, required int NOT NULL CHECK(required BETWEEN 1 AND 2), state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected')), UNIQUE(document_type,document_id,revision));
ALTER TABLE approvals ADD COLUMN makers text[] NOT NULL DEFAULT ARRAY[]::text[];
CREATE TABLE decisions(id text PRIMARY KEY, approval_id text NOT NULL REFERENCES approvals, actor text NOT NULL REFERENCES users, accept boolean NOT NULL, reason text NOT NULL, at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(approval_id,actor));
CREATE TABLE credit_memos(id text PRIMARY KEY, request_id text NOT NULL UNIQUE REFERENCES credit_requests, invoice_id text NOT NULL REFERENCES invoices, number bigint NOT NULL UNIQUE, amount_cents bigint NOT NULL CHECK(amount_cents>0), created_at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE TABLE journal_entries(id text PRIMARY KEY, business_key text NOT NULL UNIQUE, invoice_id text NOT NULL REFERENCES invoices, month date NOT NULL REFERENCES periods, kind text NOT NULL CHECK(kind IN ('issue','credit','void')), created_xid bigint NOT NULL DEFAULT txid_current(), created_at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE TABLE journal_lines(id bigserial PRIMARY KEY, entry_id text NOT NULL REFERENCES journal_entries, customer_id text NOT NULL REFERENCES customers, account text NOT NULL CHECK(account IN ('1100','4000','4900')), direction text NOT NULL CHECK(direction IN ('D','C')), amount_cents bigint NOT NULL CHECK(amount_cents>0));
CREATE FUNCTION immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable record' USING ERRCODE='23514'; END $$;
CREATE TRIGGER entries_immutable BEFORE UPDATE OR DELETE ON journal_entries FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE TRIGGER lines_immutable BEFORE UPDATE OR DELETE ON journal_lines FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE TRIGGER credits_immutable BEFORE UPDATE OR DELETE ON credit_memos FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE TRIGGER contracts_immutable BEFORE UPDATE OR DELETE ON contracts FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE TRIGGER decisions_immutable BEFORE UPDATE OR DELETE ON decisions FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE FUNCTION line_creation_boundary() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM journal_entries WHERE id=NEW.entry_id AND created_xid=txid_current()) THEN RAISE EXCEPTION 'cannot append to committed journal' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER line_creation_boundary BEFORE INSERT ON journal_lines FOR EACH ROW EXECUTE FUNCTION line_creation_boundary();
CREATE FUNCTION balanced() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE eid text; d numeric; c numeric; BEGIN
 eid:=NEW.id; SELECT coalesce(sum(amount_cents) FILTER(WHERE direction='D'),0), coalesce(sum(amount_cents) FILTER(WHERE direction='C'),0) INTO d,c FROM journal_lines WHERE entry_id=eid;
 IF d<>c OR d=0 THEN RAISE EXCEPTION 'unbalanced or empty journal %',eid USING ERRCODE='23514'; END IF; RETURN NULL; END $$;
CREATE CONSTRAINT TRIGGER balanced AFTER INSERT ON journal_entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION balanced();
CREATE FUNCTION posting_period() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE s text; BEGIN SELECT state INTO s FROM periods WHERE month=NEW.month FOR UPDATE; IF s IS DISTINCT FROM 'open' THEN RAISE EXCEPTION 'period closed' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER posting_period BEFORE INSERT ON journal_entries FOR EACH ROW EXECUTE FUNCTION posting_period();
CREATE FUNCTION protect_invoice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.state IN ('issued','void') AND (NEW.customer_id,NEW.contract_id,NEW.month,NEW.kind,NEW.source_key,NEW.original_id,NEW.revision,NEW.impressions,NEW.rate,NEW.total_cents,NEW.snapshot,NEW.created_by,NEW.number,NEW.issued_at,NEW.reason,NEW.makers) IS DISTINCT FROM (OLD.customer_id,OLD.contract_id,OLD.month,OLD.kind,OLD.source_key,OLD.original_id,OLD.revision,OLD.impressions,OLD.rate,OLD.total_cents,OLD.snapshot,OLD.created_by,OLD.number,OLD.issued_at,OLD.reason,OLD.makers) THEN RAISE EXCEPTION 'issued content immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state='void' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'void immutable' USING ERRCODE='23514'; END IF;
 IF NEW.state<>OLD.state AND NOT ((OLD.state='draft' AND NEW.state='pending') OR (OLD.state='pending' AND NEW.state IN ('draft','approved')) OR (OLD.state='approved' AND NEW.state IN ('draft','issued')) OR (OLD.state='issued' AND NEW.state='void')) THEN RAISE EXCEPTION 'invalid transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='issued' AND OLD.state<>'issued' AND (NEW.number IS NULL OR NOT EXISTS(SELECT 1 FROM approvals WHERE document_type='invoice' AND document_id=NEW.id AND revision=NEW.revision AND state='approved')) THEN RAISE EXCEPTION 'approval required' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER protect_invoice BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION protect_invoice();
CREATE FUNCTION check_decision() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE a approvals; r text; BEGIN
 SELECT * INTO a FROM approvals WHERE id=NEW.approval_id FOR UPDATE;
 SELECT role INTO r FROM users WHERE id=NEW.actor AND active;
 IF a.state<>'pending' OR (NEW.actor=a.creator OR NEW.actor=ANY(a.makers)) OR r NOT IN ('approver','controller') OR r IS NULL OR (a.required=2 AND r<>'controller') THEN RAISE EXCEPTION 'ineligible approval' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER check_decision BEFORE INSERT ON decisions FOR EACH ROW EXECUTE FUNCTION check_decision();
CREATE TABLE outbox(id text PRIMARY KEY, document_id text NOT NULL, invoice_id text NOT NULL REFERENCES invoices, month date NOT NULL REFERENCES periods, document_type text NOT NULL, sequence bigint NOT NULL, payload jsonb NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','dead')), attempts int NOT NULL DEFAULT 0, claim_token text, locked_until timestamptz, next_at timestamptz NOT NULL DEFAULT now(), last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(document_id,sequence));
CREATE INDEX outbox_due ON outbox(next_at) WHERE status='pending';
CREATE TABLE audit(id bigserial PRIMARY KEY, actor text NOT NULL, action text NOT NULL, entity text NOT NULL, details jsonb NOT NULL, at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE ON audit FOR EACH ROW EXECUTE FUNCTION immutable();
CREATE TABLE idempotency(actor text NOT NULL, operation text NOT NULL, key text NOT NULL, hash text NOT NULL, response json, PRIMARY KEY(actor,operation,key));
CREATE TABLE recon_runs(id bigserial PRIMARY KEY, month date NOT NULL REFERENCES periods, generation bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), source_hash text NOT NULL, findings int NOT NULL);
CREATE TABLE breaks(id text PRIMARY KEY, month date NOT NULL REFERENCES periods, kind text NOT NULL, subject text NOT NULL, expected text NOT NULL, actual text NOT NULL, state text NOT NULL CHECK(state IN ('open','resolved')), occurrences int NOT NULL DEFAULT 1, run_id bigint NOT NULL REFERENCES recon_runs, UNIQUE(month,kind,subject));
CREATE TABLE sessions(hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users, csrf text NOT NULL, expires timestamptz NOT NULL);
CREATE TABLE oidc_states(hash text PRIMARY KEY, nonce text NOT NULL, verifier text NOT NULL, expires timestamptz NOT NULL);
CREATE SCHEMA erp;
CREATE TABLE erp.records(id text PRIMARY KEY, sequence bigint NOT NULL, payload jsonb NOT NULL, hash text NOT NULL);
CREATE TABLE erp.control(id int PRIMARY KEY DEFAULT 1 CHECK(id=1), mode text NOT NULL DEFAULT 'normal');
INSERT INTO erp.control DEFAULT VALUES;
-- Runtime identity cannot alter schema, roles, or history. Migrations use an owner connection.
DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='desk_app') THEN CREATE ROLE desk_app NOLOGIN; END IF; END $$;
GRANT USAGE ON SCHEMA public TO desk_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO desk_app;
GRANT INSERT,UPDATE ON periods,invoices,counters,credit_requests,approvals,outbox,idempotency,breaks,sessions,oidc_states TO desk_app;
GRANT DELETE ON sessions,oidc_states TO desk_app;
GRANT INSERT ON decisions,credit_memos,journal_lines,audit,recon_runs TO desk_app;
GRANT INSERT(id,business_key,invoice_id,month,kind) ON journal_entries TO desk_app;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO desk_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

CREATE UNIQUE INDEX one_issue_posting ON journal_entries(invoice_id) WHERE kind='issue';
CREATE UNIQUE INDEX one_void_posting ON journal_entries(invoice_id) WHERE kind='void';
CREATE FUNCTION credit_content_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.invoice_id,NEW.month,NEW.amount_cents,NEW.reason,NEW.created_by) IS DISTINCT FROM (OLD.invoice_id,OLD.month,OLD.amount_cents,OLD.reason,OLD.created_by) THEN RAISE EXCEPTION 'credit content immutable' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER credit_content_immutable BEFORE UPDATE ON credit_requests FOR EACH ROW EXECUTE FUNCTION credit_content_immutable();
CREATE FUNCTION approval_content_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.document_type,NEW.document_id,NEW.revision,NEW.content_hash,NEW.creator,NEW.required,NEW.makers) IS DISTINCT FROM (OLD.document_type,OLD.document_id,OLD.revision,OLD.content_hash,OLD.creator,OLD.required,OLD.makers) THEN RAISE EXCEPTION 'approval content immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state<>'pending' OR (NEW.state='approved' AND (SELECT count(*) FROM decisions WHERE approval_id=NEW.id AND accept)<NEW.required) OR (NEW.state='rejected' AND NOT EXISTS(SELECT 1 FROM decisions WHERE approval_id=NEW.id AND NOT accept)) THEN RAISE EXCEPTION 'approval decision evidence missing' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER approval_content_guard BEFORE UPDATE ON approvals FOR EACH ROW EXECUTE FUNCTION approval_content_guard();
DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='desk_erp') THEN CREATE ROLE desk_erp NOLOGIN; END IF; END $$;
GRANT USAGE ON SCHEMA erp TO desk_erp;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA erp TO desk_erp;
-- Check lines too: SET CONSTRAINTS IMMEDIATE must not permit additions after
-- the entry-level deferred check has already executed within the transaction.
CREATE FUNCTION line_balanced() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE d numeric; c numeric; BEGIN
 SELECT coalesce(sum(amount_cents) FILTER(WHERE direction='D'),0),coalesce(sum(amount_cents) FILTER(WHERE direction='C'),0) INTO d,c FROM journal_lines WHERE entry_id=NEW.entry_id;
 IF d<>c OR d=0 THEN RAISE EXCEPTION 'unbalanced journal' USING ERRCODE='23514'; END IF; RETURN NULL; END $$;
CREATE CONSTRAINT TRIGGER line_balanced AFTER INSERT ON journal_lines DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION line_balanced();
CREATE FUNCTION invoice_insert_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE s text; BEGIN
 SELECT state INTO s FROM periods WHERE month=NEW.month FOR UPDATE;
 IF s IS DISTINCT FROM 'open' OR NEW.state<>'draft' OR NEW.number IS NOT NULL OR NEW.credited_cents<>0 OR NEW.revision<>1 OR NEW.version<>1 THEN RAISE EXCEPTION 'new document must be a draft in an open period' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
CREATE TRIGGER invoice_insert_guard BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION invoice_insert_guard();
