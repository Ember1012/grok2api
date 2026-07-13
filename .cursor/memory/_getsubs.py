import json
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if "subscriptionsGetSubscriptions" not in text:
        continue
    if "cdn.grok.com" not in url:
        continue
    i = text.find("subscriptionsGetSubscriptions")
    # search for path near definition in openapi client - look for GetSubscriptionsRaw
    for key in ["subscriptionsGetSubscriptionsRaw", 'path:"/rest/subscriptions"', "path:'/rest/subscriptions'"]:
        j = text.find(key)
        print("FILE", url.split("/")[-1], "key", key, "idx", j)
        if j >= 0:
            print(text[max(0,j-50):j+500].replace("\n"," ")[:550])
            print("---")
