import json
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
text = ""
for e in har["log"]["entries"]:
    if e["request"]["url"].rstrip("/") == "https://grok.com":
        text = (e["response"].get("content") or {}).get("text") or ""
        break
i = text.find("bestSubscription")
print("idx", i)
print(text[i-300:i+3500])
print("====")
# also dump rate-limits and products names mapping
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    if "rest/rate-limits" in url:
        print("RATE", (e["response"].get("content") or {}).get("text"))
