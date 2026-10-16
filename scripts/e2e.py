#!/usr/bin/env python3
"""Run the complete local stack in a disposable database; preserve the user's demo."""
from datetime import date
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]

def run(args, env):
    subprocess.run(args, cwd=ROOT, env=env, check=True)

def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]

def main():
    run_date = date.today().isoformat()
    env = os.environ.copy()
    root_url = env.get('TEST_DATABASE_URL')
    if not root_url:
        raise SystemExit('TEST_DATABASE_URL must identify a disposable-test-capable PostgreSQL login.')
    name = 'desk_e2e_' + uuid.uuid4().hex
    db_url = urllib.parse.urlsplit(root_url)._replace(path='/' + name).geturl()
    processes = []
    created = False
    evidence = ROOT / 'test-results/e2e'
    evidence.mkdir(parents=True, exist_ok=True)
    def sql(statement):
        subprocess.run(['psql', '--dbname', root_url, '-v', 'ON_ERROR_STOP=1', '-c', statement], env=env, check=True, capture_output=True)
    with tempfile.TemporaryDirectory(prefix='billing-e2e-') as folder:
        tmp = Path(folder)
        try:
            sql('CREATE DATABASE ' + name)
            created = True
            api_port, erp_port = port(), port()
            while erp_port == api_port:
                erp_port = port()
            origin = f'http://127.0.0.1:{api_port}'
            env.update(DATABASE_URL=db_url, DEMO_PERSONAS='true', APP_ORIGIN=origin,
                       LISTEN_ADDR=f'127.0.0.1:{api_port}', ERP_ADDR=f'127.0.0.1:{erp_port}',
                       ERP_URL=f'http://127.0.0.1:{erp_port}', ERP_TOKEN=uuid.uuid4().hex,
                       DESK_URL=origin)
            run(['npm', '--prefix', 'web', 'run', 'build'], env)
            run(['go', 'build', '-trimpath', '-o', str(tmp / 'desk'), './cmd/desk'], env)
            run(['go', 'build', '-trimpath', '-o', str(tmp / 'erp'), './cmd/erpsim'], env)
            run([str(tmp / 'desk'), '-mode', 'migrate'], env)
            run([str(tmp / 'desk'), '-mode', 'seed'], env)
            for binary in ('erp', 'desk'):
                log = (tmp / (binary + '.log')).open('w')
                proc = subprocess.Popen([str(tmp / binary)], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
                log.close()
                processes.append(proc)
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen(origin + '/healthz', timeout=1) as response:
                        if response.status == 200:
                            break
                except OSError:
                    if any(proc.poll() is not None for proc in processes):
                        raise RuntimeError('A server exited before readiness.')
                    time.sleep(.1)
            else:
                raise RuntimeError('Readiness timed out.')
            with urllib.request.urlopen(origin, timeout=3) as response:
                assert response.status == 200 and b'Billing Close Desk' in response.read()
            run(['npm', '--prefix', 'web', 'exec', '--', 'playwright', 'test', '--config', 'web/playwright.config.ts'], env)
            run(['python3', 'scripts/demo.py', '--url', origin, '--month', '2026-09-01', '--leave-open'], env)
            run(['python3', 'scripts/recover.py', '--url', origin, '--month', '2026-09-01'], env)
            run([str(tmp / 'desk'), '-mode', 'verify'], env)
            (evidence / f'e2e-{run_date}.json').write_text(json.dumps({
                'date': run_date, 'isolated_database': True,
                'root_and_health': 'passed', 'browser_suite': 'passed',
                'actual_http_ack_loss_dual_controller_credit': 'passed',
                'actual_http_drift_repair': 'passed', 'final_books': 'passed',
                'cloud_deployed': False,
            }, indent=2) + '\n')
        finally:
            for proc in processes:
                proc.terminate()
            for proc in processes:
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
            for log in tmp.glob('*.log'):
                shutil.copyfile(log, evidence / ('e2e-' + log.name))
            if created:
                sql('DROP DATABASE ' + name + ' WITH (FORCE)')

if __name__ == '__main__':
    main()
