import json
from pathlib import Path

def extract_sub(path):
    har = json.loads(Path(path).read_text(encoding="utf-8"))
    out = {"path": path, "entries": len(har["log"]["entries"]), "subs_ssr": None, "rest_subs": [], "logs": []}
    for e in har["log"]["entries"]:
        url = e["request"]["url"]
        text = (e["response"].get("content") or {}).get("text") or ""
        method = e["request"]["method"]
        status = e["response"]["status"]
        if "rest/subscriptions" in url and "product" not in url:
            out["rest_subs"].append((method, status, url[:160], text[:800]))
        if url.rstrip("/") == "https://grok.com" or url == "https://grok.com/":
            i = text.find("bestSubscription")
            if i >= 0:
                out["subs_ssr"] = text[i-200:i+1800]
            j = text.find("isSuperGrokUser")
            if j >= 0 and not out["subs_ssr"]:
                out["subs_ssr"] = text[j-100:j+1800]
    return out

for p in [
    r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com-free.har",
    r"E:\project\GitHub\Ember1012\grok2api\.cursor\memory\grok.com.har",
]:
    r = extract_sub(p)
    print("="*60)
    print(Path(p).name, "entries", r["entries"])
    print("rest_subs count", len(r["rest_subs"]))
    for item in r["rest_subs"]:
        print(" REST", item[0], item[1], item[2])
        print(" BODY", item[3][:500])
    print("SSR snippet:")
    print(r["subs_ssr"][:2000] if r["subs_ssr"] else "NONE")
