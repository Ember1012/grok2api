import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com-free.har")
har = json.loads(p.read_text(encoding="utf-8"))
print("entries", len(har["log"]["entries"]))
# list non-static grok.com APIs
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    if any(x in url for x in ["cdn.", "static", "gtm", "stripe", "google", "facebook", "fonts", "w3.org", "hotjar"]):
        continue
    if "grok.com" not in url and "x.ai" not in url:
        continue
    method = e["request"]["method"]
    status = e["response"]["status"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if method == "GET" and ("image" in url or ".webp" in url or ".png" in url or ".ico" in url or ".js" in url or ".css" in url):
        continue
    if "monitoring" in url or "cdn-cgi" in url or "_data" in url:
        continue
    print(method, status, url.split("?")[0][:120], "len", len(text))

# search all bodies for SuperGrok / bestSubscription / isSuperGrok / SUBSCRIPTION
tokens = ["isSuperGrokUser", "bestSubscription", "SUBSCRIPTION_TIER", "activeSubscriptions", "SuperGrok", "queryKey"]
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if "cdn.grok.com" in url and "chunks" in url:
        continue
    for t in tokens:
        if t in text:
            i = text.find(t)
            print("HIT", t, "in", e["request"]["method"], url[:100], "status", e["response"]["status"])
            print(text[max(0,i-80):i+400].replace("\n"," ")[:450])
            print("---")
            break
