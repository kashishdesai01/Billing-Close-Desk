#!/usr/bin/env python3
"""Repair a synthetic ERP mismatch through audited application commands."""
import argparse
import http.cookiejar
import json
import urllib.request
import uuid
import time

p = argparse.ArgumentParser()
p.add_argument('--url', default='http://127.0.0.1:8090')
p.add_argument('--month', default='2026-10-01')
p.add_argument('--replant', action='store_true', help='Leave another synthetic drift for the next walkthrough')
a = p.parse_args()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf = ''

def call(path, body=None):
    req = urllib.request.Request(a.url + path, data=json.dumps(body).encode() if body is not None else None, headers={'Origin': a.url, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, 'Idempotency-Key': str(uuid.uuid4())})
    with client.open(req, timeout=50) as response:
        raw = response.read()
        return json.loads(raw) if raw else {}

call('/auth/demo', {'user': 'sam'})
csrf = call('/api/me')['user']['csrf']
state = call('/api/state?month=' + a.month)
match = next((b for b in state['breaks'] if b['state'] == 'open' and b['kind'] == 'erp_mismatch'), None)
if not match:
    raise SystemExit('No open synthetic ERP mismatch in this period.')
call('/api/commands', {'operation': 'resync', 'month': a.month, 'document_id': match['subject'], 'reason': 'Reviewed source snapshot after synthetic ERP drift'})
deadline = time.monotonic() + 30
while time.monotonic() < deadline:
    call('/api/tick', {})
    if all(v['status'] == 'confirmed' for v in call('/api/state?month=' + a.month)['outbox']):
        break
    time.sleep(0.25)
else:
    raise SystemExit('Delivery did not drain within 30 seconds.')
call('/api/commands', {'operation': 'reconcile', 'month': a.month})
state = call('/api/state?month=' + a.month)
resolved = next(b for b in state['breaks'] if b['id'] == match['id'])
assert resolved['state'] == 'resolved', resolved
assert state['verification']['passed'], state['verification']
if a.replant:
    call('/api/simulator', {'mode': 'drift', 'id': match['subject']})
    call('/api/commands', {'operation': 'reconcile', 'month': a.month})
    state = call('/api/state?month=' + a.month)
    assert next(b for b in state['breaks'] if b['id'] == match['id'])['occurrences'] == match['occurrences'] + 1
print(json.dumps({'recovery_verified': True, 'financial_verification': state['verification'], 'new_discrepancy_planted': a.replant}, indent=2))
