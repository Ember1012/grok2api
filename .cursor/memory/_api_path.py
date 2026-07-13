import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    if "0go8apx1-i3~v.js" not in url and "09n_o74uf9uva.js" not in url:
        continue
    text = (e["response"].get("content") or {}).get("text") or ""
    print("===", url.split("/")[-1], "len", len(text))
    # around subscriptionsGetSubscriptions
    for key in ["subscriptionsGetSubscriptions", "GetSubscriptions", "/rest/subscriptions", "subscriptions?"]:
        i = 0
        count = 0
        while True:
            j = text.find(key, i)
            if j < 0:
                break
            count += 1
            print(key, "at", j)
            print(text[max(0,j-120):j+220].replace("\n"," ")[:340])
            print("---")
            i = j + len(key)
            if count >= 5:
                break
    # openapi path patterns
    for m in re.finditer(r"subscriptions[A-Za-z0-9_]*\s*[:=]\s*function|subscriptionsGetSubscriptions\s*[:=]", text):
        print("DEF", text[m.start():m.start()+200].replace("\n"," "))
