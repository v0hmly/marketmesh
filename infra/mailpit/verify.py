"""Real SMTP delivery and persistence check in a disposable Mailpit project."""
import email.message
import json
import os
import smtplib
import sys
import time
import urllib.request

base = f"http://127.0.0.1:{os.environ['MAILPIT_UI_PORT']}"
subject = os.environ["MAILPIT_PROJECT"]
if sys.argv[1] == "send":
    message = email.message.EmailMessage()
    message["From"] = "sender@marketmesh.test"
    message["To"] = "recipient@marketmesh.test"
    message["Subject"] = subject
    message.set_content("MarketMesh local SMTP verification")
    with smtplib.SMTP("127.0.0.1", int(os.environ["MAILPIT_SMTP_PORT"]), timeout=10) as smtp:
        smtp.send_message(message)

for attempt in range(30):
    with urllib.request.urlopen(base + "/api/v1/messages", timeout=5) as response:
        messages = json.load(response)["messages"]
    found = next((item for item in messages if item["Subject"] == subject), None)
    if found:
        break
    time.sleep(0.2)
else:
    raise SystemExit("Тестовое письмо отсутствует в Mailpit")
with urllib.request.urlopen(base + "/api/v1/message/" + found["ID"], timeout=5) as response:
    message = json.load(response)
assert "MarketMesh local SMTP verification" in message["Text"], "Тело письма повреждено"
assert message["To"][0]["Address"] == "recipient@marketmesh.test", "Получатель изменился"
with urllib.request.urlopen(base + "/", timeout=5) as response:
    assert response.status == 200 and b"Mailpit" in response.read(), "Web UI недоступен"
print("Mailpit: SMTP, API, web UI и сохранённое письмо проверены (" + sys.argv[1] + ")")
