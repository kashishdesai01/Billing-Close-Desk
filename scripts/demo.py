#!/usr/bin/env python3
"""Exercise the public HTTP API. Uses only synthetic seeded records.
Run against a fresh local demo. Does not reset or delete existing data.
"""
import argparse
import http.cookiejar
import json
import urllib.request
import urllib.error
import uuid
import time

p = argparse.ArgumentParser()
p.add_argument('--url', default='http://127.0.0.1:8090')
p.add_argument('--month', default='2026-10-01')
p.add_argument('--leave-open', action='store_true', help='Leave an ERP drift break for a hands-on recovery demo')
a = p.parse_args()
jar = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
csrf = ''

def request(path, body=None):
    headers = {'Origin': a.url, 'Content-Type': 'application/json',
               'X-CSRF-Token': csrf, 'Idempotency-Key': str(uuid.uuid4())}
    r = urllib.request.Request(a.url + path, data=json.dumps(body).encode() if body is not None else None, headers=headers)
    try:
        with client.open(r, timeout=50) as response:
            raw = response.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raise SystemExit(f'{path}: HTTP {e.code}: {e.read().decode()}') from e

def login(user):
    global csrf
    request('/auth/demo', {'user': user})
    csrf = request('/api/me')['user']['csrf']

def command(operation, **fields):
    return request('/api/commands', {'operation': operation, 'month': a.month, **fields})

def state():
    return request('/api/state?month=' + a.month)

def drain():
    # Another worker may own a row, or a previous retry may not be due yet.
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        request('/api/tick', {})
        deliveries = state()['outbox']
        if all(v['status'] == 'confirmed' for v in deliveries):
            return
        if any(v['status'] == 'dead' for v in deliveries):
            raise SystemExit('Delivery reached dead state; inspect Integrations.')
        time.sleep(0.25)
    raise SystemExit('Delivery did not drain within 30 seconds.')

def invoice(i):
    return next(v for v in state()['invoices'] if v['id'] == i)

login('maya')
if state()['invoices']:
    raise SystemExit('Choose an untouched seeded month; the demo never deletes existing records.')
ids = []
for customer in state()['customers']:
    login('maya')
    result = command('preview', customer=customer['id'])
    i = result['invoice_id']
    ids.append(i)
    approval = command('submit', invoice_id=i, version=invoice(i)['version'])['approval_id']
    login('alex')
    command('decide', approval_id=approval, accept=True)
    login('sam')
    command('issue', invoice_id=i, version=invoice(i)['version'])
# Demonstrate acknowledgement loss, then recovery using the same receiver identities.
request('/api/simulator', {'mode': 'accept_then_fail'})
request('/api/tick', {})
request('/api/simulator', {'mode': 'normal'})
drain()
login('maya')
credit = command('credit', invoice_id=ids[0], version=invoice(ids[0])['version'], amount_cents=30000, reason='Approved synthetic service adjustment')
for user in ['sam', 'jules']:
    login(user)
    command('decide', approval_id=credit['approval_id'], accept=True)
login('sam')
command('apply_credit', credit_id=credit['credit_id'])
drain()
command('reconcile')
if a.leave_open:
    request('/api/simulator', {'mode': 'drift', 'id': ids[0]})
    command('reconcile')
else:
    command('close', reason='Delivery and books reconciled for synthetic demonstration')
final = state()
assert final['verification']['passed'], final['verification']
assert next(v for v in final['periods'] if v['month'] == a.month)['state'] == ('open' if a.leave_open else 'closed')
assert all(v['status'] == 'confirmed' for v in final['outbox'])
print(json.dumps({'scenario': 'month-end close with acknowledgement loss and dual-controller credit', 'month': a.month, 'invoices': len(ids), 'deliveries': len(final['outbox']), 'verification': final['verification'], 'closed': not a.leave_open}, indent=2))
