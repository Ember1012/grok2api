import json, re
from pathlib import Path
p = Path(r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har")
har = json.loads(p.read_text(encoding="utf-8"))
for e in har["log"]["entries"]:
    url = e["request"]["url"]
    if "cdn.grok.com/_next/static/chunks/" not in url:
        continue
    text = (e["response"].get("content") or {}).get("text") or ""
    if "bestSubscription" not in text and "isSuperGrokUser" not in text:
        continue
    print("CHUNK", url.split("/")[-1])
    # find rest paths
    paths = sorted(set(re.findall(r"/rest/[A-Za-z0-9_./-]+", text)))
    for path in paths:
        if any(k in path.lower() for k in ["subscr", "billing", "product", "user", "account", "session", "auth"]):
            print(" path", path)
    # find grpc services
    grpcs = sorted(set(re.findall(r"[A-Za-z0-9_.]+/(?:Get|List|Fetch)[A-Za-z0-9_]+", text)))
    for g in grpcs:
        if any(k in g.lower() for k in ["subscr", "billing", "product", "user", "account", "entitlement", "tier"]):
            print(" grpc", g)
    # method names
    for m in sorted(set(re.findall(r"subscriptions[A-Za-z0-9_]+", text)))[:40]:
        print(" api", m)
