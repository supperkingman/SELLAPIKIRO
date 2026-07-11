import sqlite3
import json
from pathlib import Path

c = sqlite3.connect(r"C:\Users\Admin\AppData\Roaming\9router\db\data.sqlite")
c.row_factory = sqlite3.Row

def redact(o):
    if isinstance(o, dict):
        out = {}
        for k, v in o.items():
            if any(x in k.lower() for x in ["token", "secret", "password", "cookie", "authorization"]) and isinstance(v, str) and len(v) > 8:
                out[k] = f"***REDACTED len={len(v)} prefix={v[:8]}...***"
            else:
                out[k] = redact(v)
        return out
    if isinstance(o, list):
        return [redact(x) for x in o]
    return o

print("ALL CONNECTIONS:")
for row in c.execute("SELECT id, provider, authType, name, email, priority, isActive FROM providerConnections"):
    print(dict(row))

print("\nGROK FULL REDACTED:")
for row in c.execute("SELECT * FROM providerConnections WHERE provider LIKE '%grok%' COLLATE NOCASE OR provider LIKE '%xai%' COLLATE NOCASE"):
    d = {k: row[k] for k in row.keys()}
    try:
        d["data"] = json.loads(d["data"])
    except Exception:
        pass
    print(json.dumps(redact(d), ensure_ascii=False, indent=2))

# also dump by known connection ids from usage
ids = [
    "3549ba78-16c5-4434-ab10-a42ff812c888",
    "e59e054b-dec1-44d6-93d2-d1604e04f32a",
    "51764e6b-eea9-4bc4-ae1a-b61fd9003e5a",
]
print("\nBY USAGE IDS:")
for i in ids:
    row = c.execute("SELECT * FROM providerConnections WHERE id=?", (i,)).fetchone()
    if not row:
        print(i, "NOT FOUND")
        continue
    d = {k: row[k] for k in row.keys()}
    try:
        d["data"] = json.loads(d["data"])
    except Exception:
        pass
    print(json.dumps(redact(d), ensure_ascii=False, indent=2))
