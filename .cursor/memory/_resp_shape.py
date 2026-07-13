import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if "16raj1-r~ik~g.js" not in url:
        continue
    # around /rest/subscriptions
    i = text.find("/rest/subscriptions")
    while i >= 0:
        print(text[max(0,i-300):i+400].replace("\n"," ")[:700])
        print("---")
        i = text.find("/rest/subscriptions", i+1)
        if i > 0 and text.find("/rest/subscriptions", i) == i:
            pass
        if text.count("/rest/subscriptions") and i > text.find("/rest/subscriptions")+5000:
            break
    # getSubscriptionsQueryData
    for key in ["getSubscriptionsQueryData", "isSuperGrokUser", "bestSubscription"]:
        j = text.find(key)
        if j>=0:
            print("KEY", key)
            print(text[max(0,j-100):j+500].replace("\n"," ")[:600])
            print("====")
