#!/usr/bin/env python3
"""Soft-archive delivery driver — Probe 15 cases. qcom-only sandbox suite.

Targets locked PR contract (phone-keyed like detail):
  POST /api/v1/admin/drivers/{phone}/archive
  POST /api/v1/admin/drivers/{phone}/restore
  GET  /api/v1/admin/drivers?assigned_store_id=…[&include_archived=true]
  GET  /api/v1/admin/drivers/{phone}

Does not read application .go, does not hit prod, does not rebuild harness.
When archive/restore return 404, FAIL with that reason (do not invent alternate paths).
"""
from __future__ import annotations

import json
import os
import random
import subprocess
import sys
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable, Optional

BASE_URL = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")
STORE_ID = os.environ.get("QCOM_STORE_ID", "001")
OTP = "112233"
SANDBOX_RIDER = "+15550000001"  # never mutate this phone
AWS = {
    "AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID", "dummy"),
    "AWS_SECRET_ACCESS_KEY": os.environ.get("AWS_SECRET_ACCESS_KEY", "dummy"),
    "AWS_DEFAULT_REGION": os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
}
DDB_EP = os.environ.get("DDB_ENDPOINT", "http://127.0.0.1:8000")
TABLE = os.environ.get("QCOM_TABLE", "QComTable")

_admin_tok: Optional[str] = None
_run_id = time.strftime("%H%M%S") + f"{random.randint(10, 99)}"
_phone_seq = 0
_created_phones: list[str] = []
_INCLUDE_ARCHIVED_PARAM: Optional[str] = None  # discovered: include_archived | show_archived


class CaseFail(Exception):
    pass


class CaseBlock(Exception):
    pass


def http(
    method: str,
    url: str,
    body: Any = None,
    headers: Optional[dict] = None,
    timeout: float = 15,
) -> tuple[int, Any, str]:
    data = None
    hdrs = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", "replace")
            code = resp.getcode()
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        code = e.code
    except Exception as e:
        return 0, {"error": str(e)}, str(e)
    parsed: Any = raw
    if raw:
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = raw
    return code, parsed, raw


def aws_cmd(args: list[str]) -> Any:
    env = {**os.environ, **AWS, "AWS_EC2_METADATA_DISABLED": "true"}
    p = subprocess.run(["aws", *args], capture_output=True, text=True, env=env, timeout=25)
    if p.returncode != 0:
        raise RuntimeError(f"aws {' '.join(args[:6])} failed: {(p.stderr or p.stdout)[-400:]}")
    return json.loads(p.stdout) if p.stdout.strip() else {}


def _from_av(node: Any) -> Any:
    if not isinstance(node, dict):
        return node
    if "S" in node:
        return node["S"]
    if "N" in node:
        s = node["N"]
        return float(s) if "." in s else int(s)
    if "BOOL" in node:
        return node["BOOL"]
    if "NULL" in node:
        return None
    if "M" in node:
        return {k: _from_av(v) for k, v in node["M"].items()}
    if "L" in node:
        return [_from_av(x) for x in node["L"]]
    if "SS" in node:
        return list(node["SS"])
    return node


def err_code(parsed: Any) -> str:
    if isinstance(parsed, dict):
        err = parsed.get("error")
        if isinstance(err, dict):
            return str(err.get("code") or err.get("reason") or "")
        if isinstance(err, str):
            return err
        return str(parsed.get("code") or "")
    return ""


def enc_phone(phone: str) -> str:
    return urllib.parse.quote(phone.strip(), safe="")


def admin_token() -> str:
    global _admin_tok
    if _admin_tok:
        return _admin_tok
    code, parsed, raw = http(
        "POST",
        f"{BASE_URL}/api/v1/admin/login",
        {"username": "sandbox", "password": "sandboxadmin"},
    )
    if code != 200 or not isinstance(parsed, dict) or not parsed.get("token"):
        raise CaseBlock(f"admin login {code} {raw[:200]}")
    _admin_tok = str(parsed["token"])
    return _admin_tok


def ah() -> dict[str, str]:
    return {"Authorization": f"Bearer {admin_token()}"}


def ensure_health() -> None:
    code, parsed, raw = http("GET", f"{BASE_URL}/health", timeout=5)
    if code != 200 or (isinstance(raw, str) and raw.strip() != "OK" and parsed != "OK"):
        raise CaseBlock(f"/health {code} {raw[:80]}")
    try:
        aws_cmd(
            [
                "dynamodb",
                "describe-table",
                "--endpoint-url",
                DDB_EP,
                "--region",
                "us-east-1",
                "--table-name",
                TABLE,
            ]
        )
    except Exception as e:
        raise CaseBlock(f"dynamo {DDB_EP} table {TABLE}: {e}") from e


def next_phone() -> str:
    """Unique probe phones +1555001xxxx — never touch sandbox rider +15550000001."""
    global _phone_seq
    _phone_seq += 1
    # 1555001 + 4 digits from run + seq → stays in +1555001xxxx family
    n = int(_run_id[-4:]) % 9000 + 1000
    phone = f"+1555001{(n + _phone_seq) % 10000:04d}"
    if phone == SANDBOX_RIDER:
        phone = f"+1555001{(_phone_seq + 7777) % 10000:04d}"
    _created_phones.append(phone)
    return phone


def create_driver(phone: str, *, name: Optional[str] = None) -> dict[str, Any]:
    body = {
        "phone_number": phone,
        "name": name or f"Probe SA {_run_id[-4:]} {_phone_seq}",
        "profile_url": "https://example.com/p.jpg",
        "nrc_url": "https://example.com/n.jpg",
        "driver_license_url": "https://example.com/l.jpg",
        "nrc_number": f"1{_phone_seq:06d}/11/1",
        "airtel_money_number": f"0977{_phone_seq:06d}"[-10:],
        "bike_number": f"PK{_phone_seq:04d}",
        "bike_brand": "Honda",
        "assigned_store_id": STORE_ID,
    }
    code, parsed, raw = http("POST", f"{BASE_URL}/api/v1/admin/drivers", body, ah())
    if code not in (200, 201) or not isinstance(parsed, dict):
        raise CaseFail(f"CreateDriver {phone} -> {code} {raw[:300]}")
    return parsed


def driver_path(phone: str, *parts: str) -> str:
    base = f"{BASE_URL}/api/v1/admin/drivers/{enc_phone(phone)}"
    if parts:
        return base + "/" + "/".join(parts)
    return base


def get_detail(phone: str) -> tuple[int, Any, str]:
    return http("GET", driver_path(phone), None, ah())


def list_drivers(
    *,
    store_id: str = STORE_ID,
    include_archived: Optional[bool] = None,
    param_name: Optional[str] = None,
) -> tuple[int, Any, str]:
    q: dict[str, str] = {"assigned_store_id": store_id}
    if include_archived:
        key = param_name or _INCLUDE_ARCHIVED_PARAM or "include_archived"
        q[key] = "true"
    qs = urllib.parse.urlencode(q)
    return http("GET", f"{BASE_URL}/api/v1/admin/drivers?{qs}", None, ah())


def drivers_list(parsed: Any) -> list[dict[str, Any]]:
    if isinstance(parsed, dict):
        ds = parsed.get("drivers")
        if isinstance(ds, list):
            return [d for d in ds if isinstance(d, dict)]
    return []


def phone_in_list(parsed: Any, phone: str) -> bool:
    return any(str(d.get("phone_number") or "") == phone for d in drivers_list(parsed))


def is_archived_detail(detail: Any) -> bool:
    if not isinstance(detail, dict):
        return False
    st = str(detail.get("status") or "").lower()
    if st in ("archived", "soft_archived", "soft-archived"):
        return True
    for k in ("archived", "is_archived", "soft_archived"):
        v = detail.get(k)
        if v is True or str(v).lower() in ("true", "1", "yes"):
            return True
    if detail.get("archived_at"):
        return True
    return False


def is_offlineish(detail: Any) -> bool:
    if not isinstance(detail, dict):
        return False
    st = str(detail.get("status") or "").lower()
    return st in ("offline", "off_duty", "off-duty", "inactive")


def is_active_not_archived(detail: Any) -> bool:
    if not isinstance(detail, dict):
        return False
    if is_archived_detail(detail):
        return False
    st = str(detail.get("status") or "").lower()
    return st not in ("archived", "soft_archived", "deleted")


def fail_if_archive_404(code: int, raw: str, action: str) -> None:
    if code == 404:
        raise CaseFail(
            f"{action} returned 404 — locked PR route missing on current binary "
            f"(POST /api/v1/admin/drivers/{{phone}}/{action}); do not invent alternate paths. raw={raw[:180]}"
        )


def archive(phone: str, headers: Optional[dict] = None) -> tuple[int, Any, str]:
    return http("POST", driver_path(phone, "archive"), {}, headers if headers is not None else ah())


def restore(phone: str, headers: Optional[dict] = None) -> tuple[int, Any, str]:
    return http("POST", driver_path(phone, "restore"), {}, headers if headers is not None else ah())


def archive_ok(phone: str) -> tuple[int, Any, str]:
    code, parsed, raw = archive(phone)
    fail_if_archive_404(code, raw, "archive")
    if code not in (200, 201, 204):
        raise CaseFail(f"archive {phone} -> {code} {err_code(parsed)} {raw[:240]}")
    return code, parsed, raw


def restore_ok(phone: str) -> tuple[int, Any, str]:
    code, parsed, raw = restore(phone)
    fail_if_archive_404(code, raw, "restore")
    if code not in (200, 201, 204):
        raise CaseFail(f"restore {phone} -> {code} {err_code(parsed)} {raw[:240]}")
    return code, parsed, raw


def discover_include_param(archived_phone: str, active_phone: str) -> str:
    """Prefer include_archived; accept show_archived if that is what live PR returns."""
    global _INCLUDE_ARCHIVED_PARAM
    if _INCLUDE_ARCHIVED_PARAM:
        return _INCLUDE_ARCHIVED_PARAM
    for key in ("include_archived", "show_archived"):
        code, parsed, _ = list_drivers(include_archived=True, param_name=key)
        if code != 200:
            continue
        if phone_in_list(parsed, archived_phone) and phone_in_list(parsed, active_phone):
            _INCLUDE_ARCHIVED_PARAM = key
            return key
    # fallback preference even if not yet working (pre-PR)
    _INCLUDE_ARCHIVED_PARAM = "include_archived"
    return _INCLUDE_ARCHIVED_PARAM


def ddb_get_de(phone: str) -> dict[str, Any]:
    out = aws_cmd(
        [
            "dynamodb",
            "get-item",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"DE!{phone}"}, "SK": {"S": "METADATA"}}),
        ]
    )
    item = out.get("Item") or {}
    return {k: _from_av(v) for k, v in item.items()}


def ddb_update(phone: str, update_expression: str, names: dict, values: dict) -> None:
    args = [
        "dynamodb",
        "update-item",
        "--endpoint-url",
        DDB_EP,
        "--region",
        "us-east-1",
        "--table-name",
        TABLE,
        "--key",
        json.dumps({"PK": {"S": f"DE!{phone}"}, "SK": {"S": "METADATA"}}),
        "--update-expression",
        update_expression,
        "--expression-attribute-values",
        json.dumps(values),
    ]
    if names:
        args.extend(["--expression-attribute-names", json.dumps(names)])
    aws_cmd(args)


def set_offline(phone: str) -> None:
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    ddb_update(
        phone,
        "SET #s = :s, assigned_store_id = :st, assigned_store_index_key = :ak, updated_at = :u "
        "REMOVE duty_index_key, current_store_id, current_trip_id, current_order_id, scan_deadline_at",
        {"#s": "status"},
        {
            ":s": {"S": "offline"},
            ":st": {"S": STORE_ID},
            ":ak": {"S": STORE_ID},
            ":u": {"S": now},
        },
    )


def set_on_duty_free(phone: str) -> None:
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    ddb_update(
        phone,
        "SET #s = :s, assigned_store_id = :st, current_store_id = :st, "
        "duty_index_key = :d, assigned_store_index_key = :ak, updated_at = :u "
        "REMOVE current_trip_id, current_order_id, scan_deadline_at",
        {"#s": "status"},
        {
            ":s": {"S": "eligible"},
            ":st": {"S": STORE_ID},
            ":d": {"S": f"DE_ONDUTY#{STORE_ID}"},
            ":ak": {"S": STORE_ID},
            ":u": {"S": now},
        },
    )


def set_cash(phone: str, amount: float) -> None:
    """Set in_hand_cash_zmw via Dynamo (admin PATCH not exposed on live binary)."""
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    ddb_update(
        phone,
        "SET in_hand_cash_zmw = :c, updated_at = :u",
        {},
        {":c": {"N": f"{amount:.2f}"}, ":u": {"S": now}},
    )


def seed_busy_trip(phone: str) -> tuple[str, str]:
    """Dynamo-seed an active trip and mark DE busy (prefer seed over Java order-service SUT)."""
    de = ddb_get_de(phone)
    de_id = str(de.get("de_id") or "")
    if not de_id:
        raise CaseFail(f"no de_id for {phone}")
    oid = f"ORDSA{_run_id}{_phone_seq:02d}"
    tid = f"TRSA{_run_id}{_phone_seq:02d}"
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    item = {
        "PK": {"S": f"TRIP!{tid}"},
        "SK": {"S": "METADATA"},
        "trip_id": {"S": tid},
        "order_id": {"S": oid},
        "trip_order_id": {"S": oid},
        "store_id": {"S": STORE_ID},
        "status": {"S": "accepted"},
        "de_phone": {"S": phone},
        "de_id": {"S": de_id},
        "assigned_at": {"S": now},
        "created_at": {"S": now},
        "updated_at": {"S": now},
        "tasks": {
            "L": [
                {
                    "M": {
                        "task_id": {"S": "P1"},
                        "type": {"S": "pickup"},
                        "status": {"S": "pending"},
                    }
                },
                {
                    "M": {
                        "task_id": {"S": "D1"},
                        "type": {"S": "drop"},
                        "status": {"S": "pending"},
                    }
                },
            ]
        },
    }
    aws_cmd(
        [
            "dynamodb",
            "put-item",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--item",
            json.dumps(item),
        ]
    )
    ddb_update(
        phone,
        "SET #s = :s, current_trip_id = :t, current_order_id = :o, current_store_id = :st, "
        "assigned_store_id = :st, assigned_store_index_key = :ak, updated_at = :u "
        "REMOVE duty_index_key",
        {"#s": "status"},
        {
            ":s": {"S": "busy"},
            ":t": {"S": tid},
            ":o": {"S": oid},
            ":st": {"S": STORE_ID},
            ":ak": {"S": STORE_ID},
            ":u": {"S": now},
        },
    )
    return tid, oid


def clear_busy(phone: str, trip_id: Optional[str] = None) -> None:
    if trip_id:
        try:
            aws_cmd(
                [
                    "dynamodb",
                    "delete-item",
                    "--endpoint-url",
                    DDB_EP,
                    "--region",
                    "us-east-1",
                    "--table-name",
                    TABLE,
                    "--key",
                    json.dumps({"PK": {"S": f"TRIP!{trip_id}"}, "SK": {"S": "METADATA"}}),
                ]
            )
        except Exception:
            pass
    set_offline(phone)


def otp_initiate(phone: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/auth/initiate-otp",
        {"phone_number": phone},
        {"X-App-Type": "de"},
    )


def otp_verify(phone: str) -> tuple[int, Any, str]:
    otp_initiate(phone)
    return http(
        "POST",
        f"{BASE_URL}/api/v1/auth/verify-otp",
        {"phone_number": phone, "otp": OTP},
        {"X-App-Type": "de"},
    )


def otp_works(phone: str) -> str:
    code, parsed, raw = otp_verify(phone)
    if code != 200 or not isinstance(parsed, dict) or not parsed.get("access_token"):
        raise CaseFail(f"OTP expected to work for {phone}: {code} {raw[:220]}")
    return str(parsed["access_token"])


def otp_blocked(phone: str) -> None:
    c1, p1, r1 = otp_initiate(phone)
    if c1 == 200 and isinstance(p1, dict) and "OTP" in str(p1.get("message") or "").upper():
        # initiate succeeded — verify must be blocked
        c2, p2, r2 = http(
            "POST",
            f"{BASE_URL}/api/v1/auth/verify-otp",
            {"phone_number": phone, "otp": OTP},
            {"X-App-Type": "de"},
        )
        if c2 == 200 and isinstance(p2, dict) and p2.get("access_token"):
            raise CaseFail(f"OTP not blocked while archived: initiate {c1} verify {c2} {r2[:180]}")
        return
    if c1 in (200,) and isinstance(p1, dict) and p1.get("access_token"):
        raise CaseFail(f"OTP initiate returned token while archived: {r1[:180]}")
    if c1 and c1 < 400 and err_code(p1) == "":
        # ambiguous success without token — try verify
        c2, p2, r2 = http(
            "POST",
            f"{BASE_URL}/api/v1/auth/verify-otp",
            {"phone_number": phone, "otp": OTP},
            {"X-App-Type": "de"},
        )
        if c2 == 200 and isinstance(p2, dict) and p2.get("access_token"):
            raise CaseFail(f"OTP not blocked (verify succeeded) while archived: {r2[:180]}")
        return
    if c1 == 0:
        raise CaseBlock(f"OTP initiate transport {r1[:200]}")
    # non-2xx / error code on initiate = blocked ✓
    if c1 >= 400 or err_code(p1):
        return
    raise CaseFail(f"OTP block unverifiable: initiate {c1} {r1[:180]}")


def assert_hidden_default(phone: str) -> None:
    code, parsed, raw = list_drivers(include_archived=False)
    if code != 200:
        raise CaseFail(f"default list {code} {raw[:180]}")
    if phone_in_list(parsed, phone):
        raise CaseFail(f"archived {phone} still on default list")


def assert_on_default(phone: str) -> None:
    code, parsed, raw = list_drivers(include_archived=False)
    if code != 200:
        raise CaseFail(f"default list {code} {raw[:180]}")
    if not phone_in_list(parsed, phone):
        raise CaseFail(f"{phone} missing from default list")


def assert_detail_archived(phone: str) -> dict[str, Any]:
    code, parsed, raw = get_detail(phone)
    if code != 200 or not isinstance(parsed, dict):
        raise CaseFail(f"detail {phone} -> {code} {raw[:200]}")
    if not is_archived_detail(parsed):
        raise CaseFail(f"detail not archived for {phone}: status={parsed.get('status')} keys={list(parsed)[:20]}")
    return parsed


def assert_detail_offline(phone: str) -> dict[str, Any]:
    code, parsed, raw = get_detail(phone)
    if code != 200 or not isinstance(parsed, dict):
        raise CaseFail(f"detail {phone} -> {code} {raw[:200]}")
    if is_archived_detail(parsed):
        raise CaseFail(f"still archived after restore: {parsed.get('status')}")
    if not is_offlineish(parsed):
        # after restore contract: default list as offline — status should be offline
        raise CaseFail(f"expected offline after restore, got status={parsed.get('status')}")
    return parsed


def rider_token(phone: str) -> str:
    return otp_works(phone)


def duty_start(token: str) -> tuple[int, Any, str]:
    code, qr, raw = http("GET", f"{BASE_URL}/api/v1/stores/{STORE_ID}/qr")
    qr_code = qr.get("qr_code") if isinstance(qr, dict) else None
    body = {
        "qr_code": qr_code or f"{STORE_ID}probe",
        "lat": -15.4167,
        "lng": 28.2833,
        "accuracy_m": 10,
        "is_mocked": False,
    }
    return http(
        "POST",
        f"{BASE_URL}/api/v1/de/duty/start",
        body,
        {"Authorization": f"Bearer {token}", "X-App-Type": "de"},
    )


# ---------- cases ----------


def tc01() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    assert_hidden_default(phone)
    assert_detail_archived(phone)
    return f"archive offline {phone} hidden + detail archived"


def tc02() -> str:
    phone = next_phone()
    create_driver(phone)
    set_on_duty_free(phone)
    de0 = ddb_get_de(phone)
    if str(de0.get("status") or "").lower() not in ("eligible", "free", "on_duty", "onduty"):
        raise CaseFail(f"setup on-duty free failed status={de0.get('status')}")
    archive_ok(phone)
    assert_hidden_default(phone)
    detail = assert_detail_archived(phone)
    # forced off duty: no duty_index / not eligible
    de = ddb_get_de(phone)
    if de.get("duty_index_key"):
        raise CaseFail(f"still has duty_index_key after archive: {de.get('duty_index_key')}")
    if de.get("current_trip_id"):
        raise CaseFail(f"on trip after free archive: {de.get('current_trip_id')}")
    st = str(de.get("status") or detail.get("status") or "").lower()
    if st in ("eligible", "busy", "free"):
        raise CaseFail(f"not forced off duty: dynamo status={de.get('status')} detail={detail.get('status')}")
    return f"archive on-duty free {phone} forced offline then archived"


def tc03() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    restore_ok(phone)
    assert_on_default(phone)
    assert_detail_offline(phone)
    otp_works(phone)
    return f"restore {phone} offline on default + OTP works"


def tc04() -> str:
    active = next_phone()
    archived = next_phone()
    create_driver(active, name="Probe Active")
    create_driver(archived, name="Probe Archived")
    set_offline(active)
    set_offline(archived)
    archive_ok(archived)
    code, default, raw = list_drivers(include_archived=False)
    if code != 200:
        raise CaseFail(f"default list {code} {raw[:160]}")
    if phone_in_list(default, archived):
        raise CaseFail("default list includes archived")
    if not phone_in_list(default, active):
        raise CaseFail("default list missing active")
    param = discover_include_param(archived, active)
    code2, shown, raw2 = list_drivers(include_archived=True, param_name=param)
    if code2 != 200:
        raise CaseFail(f"include list ({param}) {code2} {raw2[:160]}")
    if not phone_in_list(shown, archived):
        raise CaseFail(f"{param}=true list missing archived {archived}")
    if not phone_in_list(shown, active):
        raise CaseFail(f"{param}=true list missing active {active}")
    return f"default excludes archived; {param}=true includes both"


def tc05() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    # detail always works even when not on default list
    assert_hidden_default(phone)
    assert_detail_archived(phone)
    return f"detail always works for archived {phone}"


def tc06() -> str:
    phone = next_phone()
    create_driver(phone)
    tid, oid = seed_busy_trip(phone)
    try:
        code, parsed, raw = archive(phone)
        fail_if_archive_404(code, raw, "archive")
        if code in (200, 201, 204):
            raise CaseFail(f"busy archive should be refused, got {code} {raw[:200]}")
        detail_code, detail, _ = get_detail(phone)
        if detail_code != 200:
            raise CaseFail(f"detail after refused archive {detail_code}")
        if is_archived_detail(detail):
            raise CaseFail("busy driver became archived")
        de = ddb_get_de(phone)
        if str(de.get("status") or "").lower() != "busy":
            raise CaseFail(f"expected still busy, got {de.get('status')}")
        if de.get("current_trip_id") != tid or de.get("current_order_id") != oid:
            raise CaseFail(f"lost trip binding trip={de.get('current_trip_id')} order={de.get('current_order_id')}")
        assert_on_default(phone)
        return f"busy archive refused {phone} code={code} {err_code(parsed)}"
    finally:
        clear_busy(phone, tid)


def tc07() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    otp_blocked(phone)
    return f"OTP blocked while archived {phone}"


def tc08() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    body = {
        "phone_number": phone,
        "name": "Second Driver",
        "profile_url": "https://example.com/p2.jpg",
        "nrc_url": "https://example.com/n2.jpg",
        "driver_license_url": "https://example.com/l2.jpg",
        "nrc_number": "999999/11/1",
        "airtel_money_number": "09770000999",
        "bike_number": "ZZ999",
        "bike_brand": "Yamaha",
        "assigned_store_id": STORE_ID,
    }
    code, parsed, raw = http("POST", f"{BASE_URL}/api/v1/admin/drivers", body, ah())
    if code in (200, 201):
        raise CaseFail(f"CreateDriver accepted archived phone {phone}: {raw[:200]}")
    ec = err_code(parsed).upper()
    if ec and ec not in (
        "DE_ALREADY_EXISTS",
        "PHONE_TAKEN",
        "ALREADY_EXISTS",
        "CONFLICT",
        "DUPLICATE",
        "PHONE_IN_USE",
    ):
        # still refused is OK if message mentions taken/exists
        msg = str(parsed).lower()
        if not any(w in msg for w in ("exist", "taken", "already", "duplicate", "conflict")):
            raise CaseFail(f"CreateDriver refuse unclear: {code} {raw[:240]}")
    assert_detail_archived(phone)
    return f"CreateDriver archived phone refused {code} {ec or 'msg'}"


def tc09() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    set_cash(phone, 150.5)
    de = ddb_get_de(phone)
    cash = float(de.get("in_hand_cash_zmw") or 0)
    if cash <= 0:
        raise CaseFail(f"cash setup failed in_hand_cash_zmw={de.get('in_hand_cash_zmw')}")
    archive_ok(phone)
    assert_hidden_default(phone)
    assert_detail_archived(phone)
    return f"cash {cash} did not block archive {phone}"


def tc10() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    code, parsed, raw = archive(phone)
    fail_if_archive_404(code, raw, "archive")
    if code not in (200, 201, 204):
        raise CaseFail(f"second archive not idempotent: {code} {raw[:220]}")
    assert_detail_archived(phone)
    return f"second archive idempotent {phone}"


def tc11() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    restore_ok(phone)
    code, parsed, raw = restore(phone)
    fail_if_archive_404(code, raw, "restore")
    if code not in (200, 201, 204):
        raise CaseFail(f"second restore not idempotent: {code} {raw[:220]}")
    assert_on_default(phone)
    assert_detail_offline(phone)
    otp_works(phone)
    return f"second restore idempotent {phone}"


def tc12() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    code_a, _, raw_a = archive(phone, headers={})  # signed-out
    fail_if_archive_404(code_a, raw_a, "archive")
    if code_a in (200, 201, 204):
        raise CaseFail(f"signed-out archive succeeded: {code_a}")
    # restore while still active
    code_r, _, raw_r = restore(phone, headers={})
    fail_if_archive_404(code_r, raw_r, "restore")
    if code_r in (200, 201, 204):
        raise CaseFail(f"signed-out restore succeeded: {code_r}")
    dcode, detail, _ = get_detail(phone)
    if dcode != 200 or is_archived_detail(detail):
        raise CaseFail(f"signed-out call changed status: {detail.get('status') if isinstance(detail, dict) else detail}")
    return f"signed-out rejected archive={code_a} restore={code_r}"


def tc13() -> str:
    target = next_phone()
    rider = next_phone()
    create_driver(target)
    create_driver(rider)
    set_offline(target)
    set_offline(rider)
    tok = rider_token(rider)
    rh = {"Authorization": f"Bearer {tok}", "X-App-Type": "de"}
    code_a, _, raw_a = archive(target, headers=rh)
    fail_if_archive_404(code_a, raw_a, "archive")
    if code_a in (200, 201, 204):
        raise CaseFail(f"rider archive succeeded: {code_a}")
    # archive as admin then rider restore
    archive_ok(target)
    code_r, _, raw_r = restore(target, headers=rh)
    fail_if_archive_404(code_r, raw_r, "restore")
    if code_r in (200, 201, 204):
        raise CaseFail(f"rider restore succeeded: {code_r}")
    assert_detail_archived(target)
    return f"rider rejected archive={code_a} restore={code_r}"


def tc14() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    tok = rider_token(phone)
    set_on_duty_free(phone)
    archive_ok(phone)
    detail = assert_detail_archived(phone)
    de = ddb_get_de(phone)
    if de.get("duty_index_key"):
        raise CaseFail("still on duty after archive (duty_index_key set)")
    # assert OTP block + off duty; do not require immediate session kill
    otp_blocked(phone)
    # old session may still call Start Duty until expiry — observe but do not require failure
    duty_start(tok)  # ignore result
    restore_ok(phone)
    otp_works(phone)
    return (
        f"live session: off-duty-at-archive + OTP blocked for {phone}; "
        f"status={detail.get('status')}; old session not required to die"
    )


def tc15() -> str:
    phone = next_phone()
    create_driver(phone)
    set_offline(phone)
    archive_ok(phone)
    restore_ok(phone)
    assert_on_default(phone)
    archive_ok(phone)
    assert_hidden_default(phone)
    otp_blocked(phone)
    assert_detail_archived(phone)
    return f"restore then archive again works {phone}"


CASES: list[tuple[str, Callable[[], str]]] = [
    ("TC-01", tc01),
    ("TC-02", tc02),
    ("TC-03", tc03),
    ("TC-04", tc04),
    ("TC-05", tc05),
    ("TC-06", tc06),
    ("TC-07", tc07),
    ("TC-08", tc08),
    ("TC-09", tc09),
    ("TC-10", tc10),
    ("TC-11", tc11),
    ("TC-12", tc12),
    ("TC-13", tc13),
    ("TC-14", tc14),
    ("TC-15", tc15),
]


def run_all() -> int:
    ensure_health()
    admin_token()
    print(
        f"soft-archive suite BASE_URL={BASE_URL} store={STORE_ID} run={_run_id} "
        f"paths=POST /api/v1/admin/drivers/{{phone}}/archive|restore "
        f"GET /api/v1/admin/drivers?assigned_store_id=&include_archived= "
        f"GET /api/v1/admin/drivers/{{phone}}",
        flush=True,
    )
    results: list[tuple[str, str, str]] = []
    failed = 0
    for name, fn in CASES:
        try:
            reason = fn()
            status = "PASS"
        except CaseBlock as e:
            status, reason = "BLOCKED", str(e)
            failed += 1
        except CaseFail as e:
            status, reason = "FAIL", str(e)
            failed += 1
        except Exception as e:
            status, reason = "FAIL", f"exception {e}\n{traceback.format_exc()[-400:]}"
            failed += 1
        print(f"{name} {status} {reason}", flush=True)
        results.append((name, status, reason))
        time.sleep(0.05)
    npass = sum(1 for _, s, _ in results if s == "PASS")
    nfail = sum(1 for _, s, _ in results if s == "FAIL")
    nblock = sum(1 for _, s, _ in results if s == "BLOCKED")
    print(f"{len(results)} cases: {npass} PASS, {nfail} FAIL, {nblock} BLOCKED", flush=True)
    print(f"created_phones={_created_phones}", flush=True)
    return 1 if failed else 0


def main() -> int:
    return run_all()


if __name__ == "__main__":
    sys.exit(main())
