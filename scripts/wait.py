import time
import urllib.request
for attempt in range(60):
    try:
        with urllib.request.urlopen('http://127.0.0.1:8090/healthz', timeout=2) as response:
            if response.status == 200:
                break
    except OSError:
        time.sleep(1)
else:
    raise SystemExit('API did not become healthy')
