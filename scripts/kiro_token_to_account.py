#!/usr/bin/env python3
"""
kiro_token_to_account.py - Chuyen file token Kiro IDE (kiro-auth-token.json)
thanh 1 account trong config.json cua Kiro-Go, roi ghi vao (merge, khong ghi de).

Ho tro:
  - external_idp (Microsoft Entra / Azure AD)  -> issuerUrl chua microsoftonline
  - social (Google / GitHub)
  - idc / builderId (AWS)

Cach dung:
  python3 kiro_token_to_account.py <token.json> <config.json> [nickname]

- Neu config.json chua ton tai: tao moi voi skeleton hop le.
- Chong trung: neu da co account cung refreshToken thi CAP NHAT thay vi them moi.
- Giu nguyen moi truong khac trong config (password, apiKeys...).
"""
import json, sys, os, uuid
from datetime import datetime, timezone


def parse_expires(v):
    """Doi expiresAt (ISO8601 hoac unix) sang unix seconds (int)."""
    if v is None or v == "":
        return 0
    if isinstance(v, (int, float)):
        return int(v)
    s = str(v).strip()
    if s.isdigit():
        return int(s)
    try:
        s2 = s.replace("Z", "+00:00")
        return int(datetime.fromisoformat(s2).replace(tzinfo=timezone.utc).timestamp()) \
            if "+" not in s2 else int(datetime.fromisoformat(s2).timestamp())
    except Exception:
        return 0


def detect_auth(tok):
    """Suy ra authMethod + provider tu token."""
    issuer = (tok.get("issuerUrl") or "").lower()
    provider = tok.get("provider") or ""
    # authMethod trong token file co the viet hoa khac nhau (vd "IdC") -> so khop
    # khong phan biet hoa/thuong de tranh phan loai nham thanh "social".
    am = (tok.get("authMethod") or "").lower()
    if "microsoftonline" in issuer or am == "external_idp":
        return "external_idp", (provider if provider and provider != "ExternalIdp" else "AzureAD")
    if provider.lower() in ("google", "github"):
        return "social", provider
    if am in ("idc", "builderid", "iam"):
        return "idc", (provider or "BuilderId")
    # Mac dinh: neu co issuer ngoai -> external_idp, con lai social
    if issuer:
        return "external_idp", (provider or "AzureAD")
    return "social", (provider or "Kiro SSO")


def load_idc_client_creds(tok, token_path):
    """Doc clientId + clientSecret cho account idc/builderId.

    Refresh idc/builderId qua OIDC endpoint cua AWS CAN ca clientId lan
    clientSecret. Token file chi luu 'clientIdHash' (khong phai secret) - credential
    that nam o file dang ky client rieng: <cache_dir>/<clientIdHash>.json.
    Tra ve (clientId, clientSecret); rong neu khong tim thay.
    """
    cid = tok.get("clientId", "") or ""
    csec = ""
    chash = tok.get("clientIdHash")
    if chash:
        reg_path = os.path.join(os.path.dirname(os.path.abspath(token_path)), chash + ".json")
        if os.path.exists(reg_path):
            try:
                with open(reg_path, "r", encoding="utf-8-sig") as f:
                    reg = json.load(f)
                cid = reg.get("clientId", cid) or cid
                csec = reg.get("clientSecret", "") or ""
            except Exception:
                pass
    return cid, csec


def main():
    if len(sys.argv) < 3:
        print("Dung: python3 kiro_token_to_account.py <token.json> <config.json> [nickname]")
        sys.exit(1)
    token_path, config_path = sys.argv[1], sys.argv[2]
    nickname = sys.argv[3] if len(sys.argv) > 3 else ""

    with open(token_path, "r", encoding="utf-8-sig") as f:
        tok = json.load(f)

    refresh = tok.get("refreshToken")
    if not refresh:
        print("LOI: token thieu refreshToken - khong dung duoc.")
        sys.exit(1)

    auth_method, provider = detect_auth(tok)

    account = {
        "id": str(uuid.uuid4()),
        "nickname": nickname or (provider + " account"),
        "accessToken": tok.get("accessToken", ""),
        "refreshToken": refresh,
        "clientId": tok.get("clientId", ""),
        "authMethod": auth_method,
        "provider": provider,
        "region": tok.get("region", "") or "us-east-1",
        "expiresAt": parse_expires(tok.get("expiresAt")),
        "enabled": True,
    }
    # idc/builderId: refresh qua OIDC endpoint CAN clientId + clientSecret.
    # Token file chi luu clientIdHash -> doc credential that tu file dang ky client.
    if auth_method == "idc":
        cid, csec = load_idc_client_creds(tok, token_path)
        account["clientId"] = cid
        if csec:
            account["clientSecret"] = csec
        else:
            print("CANH BAO: khong lay duoc clientSecret - account idc se KHONG refresh duoc.")

    # Truong danh cho external_idp (refresh material)
    if auth_method == "external_idp":
        account["tokenEndpoint"] = tok.get("tokenEndpoint", "")
        account["issuerUrl"] = tok.get("issuerUrl", "")
        account["scopes"] = tok.get("scopes", "")

    # Doc config hien co (hoac tao skeleton)
    if os.path.exists(config_path) and os.path.getsize(config_path) > 0:
        with open(config_path, "r", encoding="utf-8-sig") as f:
            cfg = json.load(f)
    else:
        cfg = {"host": "0.0.0.0", "requireApiKey": False, "accounts": [], "apiKeys": []}

    cfg.setdefault("accounts", [])

    # Chong trung theo refreshToken
    replaced = False
    for i, a in enumerate(cfg["accounts"]):
        if a.get("refreshToken") == refresh:
            account["id"] = a.get("id", account["id"])  # giu id cu
            cfg["accounts"][i] = account
            replaced = True
            break
    if not replaced:
        cfg["accounts"].append(account)

    with open(config_path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)

    action = "CAP NHAT" if replaced else "THEM MOI"
    print(f"OK [{action}] account: authMethod={auth_method}, provider={provider}, "
          f"nickname={account['nickname']}")
    print(f"Tong so account hien co: {len(cfg['accounts'])}")


if __name__ == "__main__":
    main()
