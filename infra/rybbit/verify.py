"""Check ingestion and durable events via the public local API, without SQL seeds."""
import json
import os
import time
import urllib.parse
import urllib.request
from bootstrap import api, base, login, root

site_id = (root / "site-id").read_text().strip()
login()
if os.sys.argv[1] == "send":
    payload = {"site_id": site_id, "type": "custom_event", "pathname": "/login", "hostname": "localhost", "event_name": "login_succeeded", "querystring": "", "referrer": ""}
    request = urllib.request.Request(base + "/api/track", data=json.dumps(payload).encode(), headers={"Content-Type": "application/json", "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"})
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 200:
            raise SystemExit("Rybbit не принял событие")

query = urllib.parse.urlencode({"timeZone": "UTC", "pastMinutes": "60", "page_size": "100"})
for attempt in range(45):
    result = api(f"/api/sites/{site_id}/events?{query}")
    events = result.get("data", [])
    if any(item.get("event_name") == "login_succeeded" for item in events):
        break
    time.sleep(1)
else:
    raise SystemExit("Событие не появилось в аналитике")
for item in events:
    if item.get("event_name") == "login_succeeded":
        assert not item.get("querystring") and not item.get("referrer"), "Лишние данные в аналитике"
with urllib.request.urlopen(base, timeout=10) as response:
    assert response.status == 200, "Dashboard недоступен"
print("Rybbit: приём события, чтение аналитики и dashboard проверены")
