import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    text = (e["response"].get("content") or {}).get("text") or ""
    if "getSubscriptionsQueryData" not in text and "isSuperGrokUser" not in text:
        continue
    if "cdn.grok.com" not in url:
        continue
    name = url.split("/")[-1]
    # find function getSubscriptionsQueryData
    for key in ["getSubscriptionsQueryData", "function getSubscriptions", "isSuperGrokUser:!0", "isSuperGrokUser:!", "bestSubscription:"]:
        idx = 0
        count = 0
        while count < 3:
            j = text.find(key, idx)
            if j < 0:
                break
            print("FILE", name, "KEY", key, "at", j)
            print(text[max(0,j-80):j+700].replace("\n"," ")[:780])
            print("====")
            idx = j + len(key)
            count += 1
