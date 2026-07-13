import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
# search all chunk bodies for rest path near GetSubscriptions
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if "subscriptionsGetSubscriptions" not in text:
        continue
    print("FILE", url.split("/")[-1] if "http" in url else url)
    # extract nearby string literals containing rest or subscriptions
    idx = text.find("subscriptionsGetSubscriptions")
    window = text[max(0,idx-2000):idx+2000]
    strs = re.findall(r'"([^"\\]{3,120})"', window)
    for s in strs:
        if any(k in s.lower() for k in ["subscr", "rest", "billing", "product", "get", "http"]):
            print(" str", s)
    strs2 = re.findall(r"'([^'\\]{3,120})'", window)
    for s in strs2:
        if any(k in s.lower() for k in ["subscr", "rest", "billing", "product", "get", "http"]):
            print(" str2", s)
