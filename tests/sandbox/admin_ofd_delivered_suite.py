#!/usr/bin/env python3
"""Admin OFD / Delivered via Java → qcom. 17 Probe cases. Isolated sandbox only.

qcom-only suite. Hits POST /internal/v1/trips/complete-by-order.
Does not test Java order-service or inventory. Dashboard never calls qcom.

Java tells qcom via locked contract:
  POST /internal/v1/trips/complete-by-order
  {"order_id":"<ORD…>","status":"OUT_FOR_DELIVERY"|"DELIVERED"}
  200 {"updated":true} or {"updated":false,"reason":"no_active_trip"|"no_rider"|"already_done"|"trip_terminal"}
No fallback to admin pickup/drop completes. Dashboard never calls qcom.
Rider packed gate stays exact READY_FOR_DELIVERY.
"""
from __future__ import annotations

import argparse
import json
import os
import socket
import ssl
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Optional

BASE_URL = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")
STUB_URL = os.environ.get("JAVA_STUB_URL", "http://127.0.0.1:18080").rstrip("/")
STUB_PORT = int(os.environ.get("JAVA_STUB_PORT", "18080"))
MAPS_PROXY_PORT = int(os.environ.get("MAPS_PROXY_PORT", "18081"))
CERT_DIR = os.environ.get("QCOM_CERT_DIR", "/workspace/bunzo/sandbox-qcom-pr9/certs")
STORE_ID = os.environ.get("QCOM_STORE_ID", "001")
RIDER_PHONE = "+15550000001"
DE_ID = "DE0458047115"
OTP = "112233"
DEAD_QCOM = "http://127.0.0.1:9"
QCOM_LOG = os.environ.get(
    "QCOM_SERVER_LOG",
    "/workspace/bunzo/sandbox-qcom-pr9/qcom-server.log",
)
AWS = {
    "AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID", "dummy"),
    "AWS_SECRET_ACCESS_KEY": os.environ.get("AWS_SECRET_ACCESS_KEY", "dummy"),
    "AWS_DEFAULT_REGION": os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
}
DDB_EP = "http://127.0.0.1:8000"
TABLE = "QComTable"

_orders: dict[str, dict[str, Any]] = {}
_orders_lock = threading.Lock()
_req_log: list[str] = []
_ofd_calls: list[str] = []
_sync_log: list[dict[str, Any]] = []
_notify_base = BASE_URL
_stub_qcom_base = BASE_URL
_stub_ours = False
_admin_tok: Optional[str] = None
_de_tok: Optional[str] = None
_de_tok_at = 0.0
_seq = 0
_created: list[str] = []
COMPLETE_BY_ORDER = "/internal/v1/trips/complete-by-order"


def _order_payload(o: dict[str, Any]) -> dict[str, Any]:
    qty = int(o.get("ordered_qty", 4))
    fulfilled = int(o.get("fulfilled_qty", qty))
    item = {
        "sku": "MILK-1",
        "productName": "Milk",
        "name": "Milk",
        "imageUrl": "https://example.com/milk.jpg",
        "orderedQuantity": qty,
        "quantity": fulfilled,
        "fulfilledQuantity": fulfilled,
    }
    return {
        "id": o["id"],
        "orderId": o["id"],
        "orderNumber": o["id"],
        "status": o.get("status", "CONFIRMED"),
        "storeId": o.get("store_id", STORE_ID),
        "store_id": o.get("store_id", STORE_ID),
        "items": [item],
        "delivery": {
            "address": "1 Sandbox Rd, Lusaka",
            "latitude": -15.41,
            "longitude": 28.31,
            "phone": "+260955000001",
            "name": "Sandbox Customer",
        },
        "paymentMethod": o.get("payment_method", "COD"),
        "grandTotal": float(o.get("grand_total", 120.0)),
        "currency": "ZMW",
    }


class JavaStubHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt: str, *args: Any) -> None:
        _req_log.append(fmt % args if args else fmt)

    def _read_json(self) -> dict[str, Any]:
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n).decode("utf-8") if n else "{}"
        try:
            parsed = json.loads(raw or "{}")
        except json.JSONDecodeError:
            parsed = {}
        return parsed if isinstance(parsed, dict) else {}

    def _send(self, code: int, body: Any, ctype: str = "application/json") -> None:
        data = body if isinstance(body, (bytes, bytearray)) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        qs = urllib.parse.parse_qs(parsed.query)
        if path in ("/_control/health", "/health"):
            self._send(200, {"ok": True, "orders": list(_orders), "qcom_base": _stub_qcom_base})
            return
        if path == "/_control/log":
            self._send(200, {"log": _req_log[-200:], "ofd": _ofd_calls[-200:], "sync": _sync_log[-200:]})
            return
        if path == "/_control/qcom-base":
            self._send(200, {"qcom_base": _stub_qcom_base})
            return
        for prefix in ("/order-service/api/v1/orders/store/", "/api/v1/orders/store/"):
            if path.startswith(prefix) and path.endswith("/by-statuses"):
                rest = path[len(prefix) : -len("/by-statuses")]
                store = rest.strip("/")
                wanted = qs.get("statuses") or qs.get("status") or []
                wanted_u = {s.strip().upper() for part in wanted for s in part.split(",") if s.strip()}
                content = []
                with _orders_lock:
                    for o in _orders.values():
                        if str(o.get("store_id", STORE_ID)) != store:
                            continue
                        st = str(o.get("status", "")).upper()
                        if wanted_u and st not in wanted_u:
                            continue
                        content.append(_order_payload(o))
                self._send(200, {"content": content, "meta": {}})
                return
        for prefix in ("/order-service/api/v1/orders/", "/api/v1/orders/"):
            if path.startswith(prefix) and "/store/" not in path:
                oid = path[len(prefix) :].strip("/")
                if "/" in oid:
                    continue
                with _orders_lock:
                    o = _orders.get(oid)
                if not o:
                    self._send(404, {"error": "not_found", "path": path})
                    return
                self._send(200, _order_payload(o))
                return
        self._send(404, {"error": "not_found", "path": path})

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        body = self._read_json()
        if path == "/_control/qcom-base":
            global _stub_qcom_base
            _stub_qcom_base = str(body.get("qcom_base") or body.get("base") or _stub_qcom_base).rstrip("/")
            self._send(200, {"qcom_base": _stub_qcom_base})
            return
        for prefix in ("/order-service/api/v1/orders/", "/api/v1/orders/"):
            if path.startswith(prefix) and path.endswith("/status"):
                oid = path[len(prefix) : -len("/status")].strip("/")
                st = str(body.get("status") or "OUT_FOR_DELIVERY")
                actor = str(body.get("actor") or self.headers.get("X-Actor") or "")
                with _orders_lock:
                    if oid not in _orders:
                        self._send(404, {"error": "not_found"})
                        return
                    _orders[oid]["status"] = st
                if st.upper() == "OUT_FOR_DELIVERY":
                    _ofd_calls.append(f"{oid}:{actor or 'none'}")
                # Admin actor only: Java tells qcom. Rider/qcom POSTs must not loop.
                if _is_admin_actor(actor) and st.upper() in ("OUT_FOR_DELIVERY", "OFD", "DELIVERED"):
                    apply_java_status_to_qcom(oid, st, qcom_base=_stub_qcom_base)
                self._send(200, {"status": "OK"})
                return
        if path == "/_control/orders":
            self._send(400, {"error": "use PUT"})
            return
        self._send(404, {"error": "not_found", "path": path})

    def do_PUT(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        body = self._read_json()
        if path.startswith("/_control/orders/"):
            oid = path[len("/_control/orders/") :].strip("/")
            if not oid:
                self._send(400, {"error": "missing id"})
                return
            with _orders_lock:
                cur = _orders.get(oid) or {"id": oid, "store_id": STORE_ID, "status": "CONFIRMED"}
                for k, v in body.items():
                    cur[k] = v
                cur["id"] = oid
                if "store_id" not in cur:
                    cur["store_id"] = STORE_ID
                _orders[oid] = cur
                payload = dict(cur)
            self._send(200, _order_payload(payload))
            return
        self._send(404, {"error": "not_found"})

    def do_DELETE(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path == "/_control/orders":
            with _orders_lock:
                _orders.clear()
            self._send(200, {"ok": True})
            return
        if path.startswith("/_control/orders/"):
            oid = path[len("/_control/orders/") :].strip("/")
            with _orders_lock:
                if oid in _orders:
                    del _orders[oid]
                    self._send(200, {"ok": True})
                    return
            self._send(404, {"error": "not_found"})
            return
        self._send(404, {"error": "not_found"})


def start_java_stub(port: int = STUB_PORT) -> ThreadingHTTPServer:
    srv = ThreadingHTTPServer(("127.0.0.1", port), JavaStubHandler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    return srv


DISTANCE_JSON = json.dumps(
    {
        "status": "OK",
        "rows": [
            {
                "elements": [
                    {
                        "status": "OK",
                        "distance": {"value": 1200, "text": "1.2 km"},
                        "duration": {"value": 300, "text": "5 mins"},
                    }
                ]
            }
        ],
    }
).encode()
GEOCODE_JSON = json.dumps(
    {
        "status": "OK",
        "results": [
            {
                "formatted_address": "Lusaka",
                "geometry": {"location": {"lat": -15.4, "lng": 28.3}},
            }
        ],
    }
).encode()
_BLOCK_HOSTS = ("bunzodelivery.com", "amazonaws.com", "15.135.73.205", "internal.bunzodelivery")


def _maps_ssl_ctx() -> ssl.SSLContext:
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(os.path.join(CERT_DIR, "maps.crt"), os.path.join(CERT_DIR, "maps.key"))
    ctx.set_alpn_protocols(["http/1.1"])
    return ctx


def _handle_maps_client(conn: socket.socket, ssl_ctx: ssl.SSLContext) -> None:
    try:
        conn.settimeout(20)
        buf = b""
        while b"\r\n\r\n" not in buf and len(buf) < 65536:
            chunk = conn.recv(4096)
            if not chunk:
                break
            buf += chunk
        line = buf.split(b"\r\n", 1)[0].decode("latin1", "replace")
        parts = line.split(" ")
        if len(parts) < 2:
            conn.close()
            return
        method, target = parts[0], parts[1]
        if method != "CONNECT":
            conn.sendall(b"HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n")
            conn.close()
            return
        hostport = target.split(":")[0].lower()
        if any(b in hostport for b in _BLOCK_HOSTS):
            conn.sendall(b"HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
            conn.close()
            return
        conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        if "maps.googleapis.com" in hostport or hostport.endswith("googleapis.com"):
            ssock = ssl_ctx.wrap_socket(conn, server_side=True)
            req = b""
            while b"\r\n\r\n" not in req and len(req) < 65536:
                c = ssock.recv(4096)
                if not c:
                    break
                req += c
            first = req.split(b"\r\n", 1)[0].decode("latin1", "replace")
            path = first.split(" ")[1] if " " in first else ""
            body = GEOCODE_JSON if "geocode" in path else DISTANCE_JSON
            hdr = (
                b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
                + str(len(body)).encode()
                + b"\r\nConnection: close\r\n\r\n"
            )
            ssock.sendall(hdr + body)
            ssock.close()
            return
        conn.close()
    except Exception:
        try:
            conn.close()
        except Exception:
            pass


def start_maps_proxy(port: int = MAPS_PROXY_PORT) -> threading.Thread:
    ctx = _maps_ssl_ctx()

    def loop() -> None:
        ls = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        ls.bind(("127.0.0.1", port))
        ls.listen(50)
        while True:
            c, _ = ls.accept()
            threading.Thread(target=_handle_maps_client, args=(c, ctx), daemon=True).start()

    t = threading.Thread(target=loop, daemon=True)
    t.start()
    return t


def serve_stubs_forever() -> None:
    start_java_stub(STUB_PORT)
    if os.path.isfile(os.path.join(CERT_DIR, "maps.crt")):
        start_maps_proxy(MAPS_PROXY_PORT)
    print(f"java stub http://127.0.0.1:{STUB_PORT} maps proxy 127.0.0.1:{MAPS_PROXY_PORT}", flush=True)
    while True:
        time.sleep(3600)


class CaseFail(Exception):
    pass


class CaseBlock(Exception):
    pass


def _is_admin_actor(actor: str) -> bool:
    a = (actor or "").strip().upper()
    return a == "ADMIN" or a.startswith("ADMIN:")


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
    p = subprocess.run(["aws", *args], capture_output=True, text=True, env=env, timeout=20)
    if p.returncode != 0:
        raise RuntimeError(f"aws {' '.join(args[:6])} failed: {(p.stderr or p.stdout)[-400:]}")
    return json.loads(p.stdout) if p.stdout.strip() else {}


def ddb_query_trips(order_id: str) -> list[dict[str, Any]]:
    out = aws_cmd(
        [
            "dynamodb",
            "query",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--index-name",
            "OrderIndex",
            "--key-condition-expression",
            "trip_order_id = :o",
            "--expression-attribute-values",
            json.dumps({":o": {"S": order_id}}),
        ]
    )
    items = []
    for it in out.get("Items") or []:
        items.append({k: _from_av(v) for k, v in it.items()})
    return items


def ddb_get_de() -> dict[str, Any]:
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
            json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
        ]
    )
    item = out.get("Item") or {}
    return {k: _from_av(v) for k, v in item.items()}


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


def stub_put(oid: str, **fields: Any) -> dict[str, Any]:
    body = {"status": fields.get("status", "CONFIRMED"), "store_id": fields.get("store_id", STORE_ID)}
    body.update(fields)
    body["id"] = oid
    code, parsed, raw = http("PUT", f"{STUB_URL}/_control/orders/{oid}", body, timeout=8)
    if code != 200:
        raise CaseFail(f"stub put {oid} -> {code} {raw[:300]}")
    if oid not in _created:
        _created.append(oid)
    return parsed if isinstance(parsed, dict) else {"id": oid}


def stub_get(oid: str) -> dict[str, Any]:
    code, parsed, raw = http("GET", f"{STUB_URL}/order-service/api/v1/orders/{oid}", timeout=8)
    if code != 200:
        raise CaseFail(f"stub get {oid} -> {code} {raw[:300]}")
    return parsed if isinstance(parsed, dict) else {}


def stub_delete(oid: str) -> None:
    http("DELETE", f"{STUB_URL}/_control/orders/{oid}", timeout=5)


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
    _admin_tok = parsed["token"]
    return _admin_tok


def de_token() -> str:
    global _de_tok, _de_tok_at
    if _de_tok and time.time() - _de_tok_at < 600:
        return _de_tok
    http(
        "POST",
        f"{BASE_URL}/api/v1/auth/initiate-otp",
        {"phone_number": RIDER_PHONE},
        {"X-App-Type": "de"},
    )
    code, parsed, raw = http(
        "POST",
        f"{BASE_URL}/api/v1/auth/verify-otp",
        {"phone_number": RIDER_PHONE, "otp": OTP},
        {"X-App-Type": "de"},
    )
    if code != 200 or not isinstance(parsed, dict) or not parsed.get("access_token"):
        raise CaseBlock(f"de login {code} {raw[:200]}")
    _de_tok = parsed["access_token"]
    _de_tok_at = time.time()
    return _de_tok


def ah() -> dict[str, str]:
    return {"Authorization": f"Bearer {admin_token()}"}


def dh() -> dict[str, str]:
    return {"Authorization": f"Bearer {de_token()}", "X-App-Type": "de"}


def set_rider(*, on_duty: bool = True, free: bool = True) -> None:
    """Seed DE METADATA so assignment FIFO sees a known duty/free state."""
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    if on_duty:
        expr = (
            "SET #s = :s, assigned_store_id = :st, current_store_id = :st, "
            "duty_index_key = :d, assigned_store_index_key = :ak, updated_at = :u"
        )
        eav = {
            ":s": {"S": "eligible"},
            ":st": {"S": STORE_ID},
            ":d": {"S": f"DE_ONDUTY#{STORE_ID}"},
            ":ak": {"S": STORE_ID},
            ":u": {"S": now},
        }
        if free:
            expr += " REMOVE current_trip_id, current_order_id, scan_deadline_at"
        aws_cmd(
            [
                "dynamodb",
                "update-item",
                "--endpoint-url",
                DDB_EP,
                "--region",
                "us-east-1",
                "--table-name",
                TABLE,
                "--key",
                json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
                "--update-expression",
                expr,
                "--expression-attribute-names",
                json.dumps({"#s": "status"}),
                "--expression-attribute-values",
                json.dumps(eav),
            ]
        )
        return
    aws_cmd(
        [
            "dynamodb",
            "update-item",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            "SET #s = :s, assigned_store_id = :st, assigned_store_index_key = :ak, updated_at = :u "
            "REMOVE duty_index_key, current_store_id, current_trip_id, current_order_id, scan_deadline_at",
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps(
                {
                    ":s": {"S": "offline"},
                    ":st": {"S": STORE_ID},
                    ":ak": {"S": STORE_ID},
                    ":u": {"S": now},
                }
            ),
        ]
    )


def cancel_order(oid: str) -> None:
    http(
        "POST",
        f"{BASE_URL}/internal/v1/trips/cancel-by-order",
        {"order_id": oid, "reason": "sandbox-suite-reset"},
        timeout=8,
    )
    stub_delete(oid)


def wait_until(fn: Callable[[], Any], timeout: float, desc: str, interval: float = 0.25) -> Any:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = fn()
        if last:
            return last
        time.sleep(interval)
    raise CaseFail(f"timeout waiting for {desc} (last={_brief(last)})")


def _brief(x: Any) -> str:
    s = str(x)
    return s if len(s) <= 180 else s[:180]


def trips_for(oid: str, *, active: bool = False) -> list[dict[str, Any]]:
    items = ddb_query_trips(oid)
    if active:
        items = [t for t in items if str(t.get("status") or "").lower() not in ("cancelled", "canceled", "completed")]
    return items


def wait_trip(oid: str, timeout: float = 20) -> dict[str, Any]:
    def _f() -> Any:
        ts = trips_for(oid, active=True)
        return ts[0] if ts else None

    return wait_until(_f, timeout, f"trip for {oid}")


def pickup_task_id(trip: dict[str, Any]) -> str:
    for t in trip.get("tasks") or []:
        if str(t.get("type") or "").lower() == "pickup":
            return str(t.get("task_id") or t.get("id") or "P1")
    return "P1"


def drop_task(trip: dict[str, Any]) -> dict[str, Any] | None:
    for t in trip.get("tasks") or []:
        if str(t.get("type") or "").lower() == "drop":
            return t
    return None


def task_status(trip: dict[str, Any], typ: str) -> str:
    for t in trip.get("tasks") or []:
        if str(t.get("type") or "").lower() == typ.lower():
            return str(t.get("status") or "")
    return ""


def err_code(parsed: Any) -> str:
    if isinstance(parsed, dict):
        err = parsed.get("error")
        if isinstance(err, dict):
            return str(err.get("code") or err.get("reason") or "")
        if isinstance(err, str):
            return err
        return str(parsed.get("code") or "")
    return str(parsed)


def new_oid(tag: str) -> str:
    global _seq
    _seq += 1
    oid = f"ORDAOD{tag}{time.strftime('%H%M%S')}{_seq:02d}"
    _created.append(oid)
    return oid


def ensure_health() -> None:
    code, parsed, raw = http("GET", f"{BASE_URL}/health", timeout=5)
    if code != 200 or (isinstance(raw, str) and raw.strip() != "OK" and parsed != "OK"):
        raise CaseBlock(f"/health {code} {raw[:80]}")
    code, _, raw = http("GET", f"{STUB_URL}/_control/health", timeout=5)
    if code != 200:
        raise CaseBlock(f"java stub not up at {STUB_URL}: {code} {raw[:80]}")


def admin_assign(oid: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/admin/assign",
        {"order_id": oid, "driver_phone": RIDER_PHONE},
        ah(),
    )


def dynamo_bind_rider(oid: str, *, accepted: bool = False) -> dict[str, Any]:
    """Put the sandbox rider on a created trip without going through cron assign."""
    ts = trips_for(oid, active=True)
    if not ts:
        raise CaseFail(f"no trip to bind for {oid}")
    trip = ts[0]
    tid = trip["trip_id"]
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    st = "accepted" if accepted else "assigned"
    aws_cmd(
        [
            "dynamodb",
            "update-item",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"TRIP!{tid}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            "SET #s = :s, de_phone = :p, de_id = :d, assigned_at = :a, updated_at = :a",
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps(
                {
                    ":s": {"S": st},
                    ":p": {"S": RIDER_PHONE},
                    ":d": {"S": DE_ID},
                    ":a": {"S": now},
                }
            ),
        ]
    )
    aws_cmd(
        [
            "dynamodb",
            "update-item",
            "--endpoint-url",
            DDB_EP,
            "--region",
            "us-east-1",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            "SET #s = :s, current_trip_id = :t, current_order_id = :o, current_store_id = :st, updated_at = :u "
            "REMOVE duty_index_key",
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps(
                {
                    ":s": {"S": "busy"},
                    ":t": {"S": tid},
                    ":o": {"S": oid},
                    ":st": {"S": STORE_ID},
                    ":u": {"S": now},
                }
            ),
        ]
    )
    got = trips_for(oid, active=True)
    return got[0] if got else trip


def accept_trip(trip_id: str, *, qcom_base: Optional[str] = None) -> tuple[int, Any, str]:
    base = (qcom_base or _notify_base).rstrip("/")
    return http("POST", f"{base}/api/v1/trip/{trip_id}/accept", {}, dh(), timeout=8)


def update_task(trip_id: str, task_id: str, status: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/trip/{trip_id}/task/{task_id}/status/update",
        {"status": status},
        dh(),
    )


def verify_pickup(trip_id: str, order_id: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/trip/{trip_id}/verify-pickup",
        {"order_id": order_id},
        dh(),
    )


def complete_pickup_rider(trip: dict[str, Any]) -> tuple[int, Any, str]:
    tid = trip["trip_id"]
    task = pickup_task_id(trip)
    code, parsed, raw = update_task(tid, task, "completed")
    if code == 200:
        return code, parsed, raw
    c2, p2, r2 = update_task(tid, task, "arrived")
    if c2 == 200:
        return update_task(tid, task, "completed")
    return code, parsed, raw


def seed_payout() -> None:
    for field, value in (
        ("rate_per_km_zmw", "10"),
        ("rate_flat_zmw", "0"),
        ("base_pay_zmw", "0"),
        ("base_pay", "0"),
        ("rate_per_km", "10"),
        ("min_pay_zmw", "10"),
    ):
        http("PATCH", f"{BASE_URL}/api/v1/config/payout", {"field": field, "value": value}, timeout=8)


def mux_404(code: int, parsed: Any, raw: str) -> bool:
    if code != 404:
        return False
    if isinstance(parsed, dict) and isinstance(parsed.get("error"), dict):
        return False
    return "page not found" in (raw or "").lower() or not isinstance(parsed, dict)


def stub_is_ours() -> bool:
    global _stub_ours
    code, parsed, _ = http("GET", f"{STUB_URL}/_control/qcom-base", timeout=3)
    _stub_ours = code == 200 and isinstance(parsed, dict) and "qcom_base" in parsed
    return _stub_ours


def set_notify_base(base: str) -> str:
    global _notify_base, _stub_qcom_base
    prev = _notify_base
    _notify_base = base.rstrip("/")
    if stub_is_ours():
        http("POST", f"{STUB_URL}/_control/qcom-base", {"qcom_base": _notify_base}, timeout=3)
    return prev


def java_write_status(oid: str, status: str, *, actor: str) -> tuple[int, Any, str]:
    """Dashboard/Java status write. Not a qcom call."""
    stub_put(oid, status=status, store_id=STORE_ID)
    return http(
        "POST",
        f"{STUB_URL}/order-service/api/v1/orders/{oid}/status",
        {"status": status, "actor": actor},
        timeout=8,
    )


def apply_java_status_to_qcom(
    order_id: str,
    status: str,
    *,
    qcom_base: Optional[str] = None,
) -> tuple[int, Any, str]:
    """Java → qcom. Locked path only. Dashboard never calls qcom."""
    base = (qcom_base or _notify_base).rstrip("/")
    st = str(status).upper().replace(" ", "_")
    if st in ("OFD", "OUT_FOR_DELIVERY"):
        st = "OUT_FOR_DELIVERY"
    elif st == "DELIVERED":
        st = "DELIVERED"
    else:
        rec = {"order_id": order_id, "status": status, "noop": "ignored_status"}
        _sync_log.append(rec)
        return 200, rec, "noop"
    rec_base = {"order_id": order_id, "status": st, "base": base}
    try:
        code, parsed, raw = http(
            "POST",
            base + COMPLETE_BY_ORDER,
            {"order_id": order_id, "status": st},
            timeout=8,
        )
        _sync_log.append({**rec_base, "path": COMPLETE_BY_ORDER, "code": code, "body": _brief(parsed)})
        return code, parsed, raw
    except Exception as e:
        rec = {**rec_base, "error": str(e), "unreachable": True}
        _sync_log.append(rec)
        return 0, rec, str(e)


def admin_mark(oid: str, status: str, *, unreachable: bool = False) -> tuple[int, Any, str]:
    """qcom-only: Java already wrote; suite hits complete-by-order. Not order-service."""
    prev = None
    if unreachable:
        prev = set_notify_base(DEAD_QCOM)
    try:
        return apply_java_status_to_qcom(oid, status)
    finally:
        if prev is not None:
            set_notify_base(prev)


def non_admin_java_status(oid: str, status: str) -> tuple[int, Any, str]:
    """DE/rider must not hit complete-by-order. qcom-only: no POST."""
    rec = {"order_id": oid, "status": status, "actor": "DE", "skipped": "non_admin"}
    return 200, rec, "skipped"


def latest_trip(oid: str) -> dict[str, Any]:
    ts = trips_for(oid)
    if not ts:
        return {}
    ts.sort(key=lambda t: str(t.get("updated_at") or t.get("created_at") or ""), reverse=True)
    return ts[0]


def active_trip(oid: str) -> dict[str, Any]:
    ts = trips_for(oid, active=True)
    return ts[0] if ts else latest_trip(oid)


def de_busy_on(oid: str) -> bool:
    de = ddb_get_de()
    st = str(de.get("status") or "").lower()
    cur = de.get("current_order_id") or de.get("current_trip_id")
    return st == "busy" and (not oid or de.get("current_order_id") == oid or bool(cur))


def de_free() -> bool:
    de = ddb_get_de()
    st = str(de.get("status") or "").lower()
    return st in ("free", "eligible") and not de.get("current_trip_id")


def seed_created(oid: str, java_status: str = "CONFIRMED") -> dict[str, Any]:
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", store_id=STORE_ID, ordered_qty=4, fulfilled_qty=4)
    trip = wait_trip(oid, 25)
    if java_status != "CONFIRMED":
        stub_put(oid, status=java_status, store_id=STORE_ID, ordered_qty=4, fulfilled_qty=4)
    return trip


def seed_assigned_accepted(oid: str, java_status: str = "READY_FOR_DELIVERY") -> dict[str, Any]:
    seed_created(oid, "CONFIRMED")
    code, parsed, raw = admin_assign(oid)
    if code != 200:
        set_rider(on_duty=True, free=True)
        time.sleep(0.2)
        code, parsed, raw = admin_assign(oid)
    if code != 200:
        raise CaseFail(f"admin assign failed {code} {raw[:300]}")
    stub_put(oid, status=java_status, store_id=STORE_ID, ordered_qty=4, fulfilled_qty=4)
    time.sleep(0.2)
    t = active_trip(oid)
    if not t:
        raise CaseFail("no trip after assign")
    return t


def seed_assigned_not_accepted(oid: str, java_status: str = "READY_FOR_DELIVERY") -> dict[str, Any]:
    seed_created(oid, "CONFIRMED")
    trip = dynamo_bind_rider(oid, accepted=False)
    stub_put(oid, status=java_status, store_id=STORE_ID, ordered_qty=4, fulfilled_qty=4)
    return active_trip(oid) or trip


def log_event_for(oid: str, event: str, since: int) -> bool:
    path = QCOM_LOG
    if not os.path.isfile(path):
        return False
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            f.seek(since)
            chunk = f.read()
    except OSError:
        return False
    for line in chunk.splitlines():
        if oid not in line or event not in line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            if f'"event":"{event}"' in line or f"event={event}" in line:
                return True
            continue
        if obj.get("event") == event and obj.get("order_id") == oid:
            return True
    return False


def log_size() -> int:
    try:
        return os.path.getsize(QCOM_LOG)
    except OSError:
        return 0


def trip_pay(trip: dict[str, Any]) -> Optional[float]:
    v = trip.get("total_pay_zmw")
    if v is None:
        return None
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def earnings_for_trip(tid: str) -> list[dict[str, Any]]:
    code, parsed, _ = http("GET", f"{BASE_URL}/api/v1/de/earnings/summary", None, dh())
    if code != 200 or not isinstance(parsed, dict):
        return []
    return [li for li in (parsed.get("line_items") or []) if li.get("reference_id") == tid]


def payment_cod(trip: dict[str, Any]) -> bool:
    pay = trip.get("payment")
    if isinstance(pay, dict):
        method = str(pay.get("method") or "").upper()
        cash = pay.get("collect_cash")
        return "COD" in method or cash is True
    return str(trip.get("payment_method") or "").upper() == "COD"


def assert_notified(oid: str, event: str, since: int) -> None:
    if log_event_for(oid, event, since):
        return
    trip = latest_trip(oid)
    for key in ("customer_notified", "notified", "notify_status", "notification"):
        if trip.get(key):
            return
    dt = drop_task(trip) or {}
    if dt.get("notified") or dt.get("customer_notified"):
        return
    if not os.path.isfile(QCOM_LOG):
        raise CaseBlock(f"customer notified unverifiable: no {QCOM_LOG} and no trip notify field")
    raise CaseBlock(
        f"customer notified unverifiable: no {event} log line for {oid} and no trip notify field"
    )


def _cleanup_case(oid: str) -> None:
    try:
        cancel_order(oid)
    except Exception:
        pass
    try:
        set_rider(on_duty=True, free=True)
    except Exception:
        pass


# ----- cases -----


def tc01() -> str:
    oid = new_oid("01")
    trip = seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    de0 = ddb_get_de()
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t = active_trip(oid)
    de = ddb_get_de()
    if str(t.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip not OFD after admin OFD status={t.get('status')} pickup={task_status(t,'pickup')}")
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(f"pickup not complete: {task_status(t,'pickup')}")
    if str(de.get("status") or "").lower() != "busy":
        raise CaseFail(f"rider freed after OFD status={de.get('status')} (was {de0.get('status')})")
    if de.get("current_trip_id") != t.get("trip_id"):
        raise CaseFail(f"rider not on trip after OFD current={de.get('current_trip_id')}")
    return f"pickup complete, trip OFD, rider stays busy on {t.get('trip_id')}"


def tc02() -> str:
    oid = new_oid("02")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t_ofd = active_trip(oid)
    tid = t_ofd.get("trip_id")
    pay0 = trip_pay(t_ofd) or 0
    lines0 = earnings_for_trip(str(tid))
    since = log_size()
    admin_mark(oid, "DELIVERED")
    t = latest_trip(oid)
    de = ddb_get_de()
    if str(t.get("status") or "").lower() != "completed":
        raise CaseFail(f"drop not complete trip={t.get('status')} drop={task_status(t,'drop')}")
    if task_status(t, "drop").lower() != "completed":
        raise CaseFail(f"drop task not completed: {task_status(t,'drop')}")
    if str(de.get("status") or "").lower() not in ("free", "eligible") or de.get("current_trip_id"):
        raise CaseFail(f"rider not freed status={de.get('status')} current={de.get('current_trip_id')}")
    pay = trip_pay(t)
    if pay is None:
        raise CaseFail("pay unverifiable: total_pay_zmw missing after drop")
    if pay <= 0:
        raise CaseFail(f"payout not recorded total_pay_zmw={pay} (before {pay0})")
    if not payment_cod(t):
        raise CaseFail(f"COD not on trip payment={t.get('payment')}")
    lines = earnings_for_trip(str(tid))
    if not lines and pay <= 0:
        raise CaseFail("earnings summary has no line for trip; payout unverifiable")
    assert_notified(oid, "ORDER_DELIVERED", since)
    return f"drop complete, rider free, pay={pay} COD, customer ORDER_DELIVERED logged"


def tc03() -> str:
    oid = new_oid("03")
    trip = seed_assigned_not_accepted(oid, "READY_FOR_DELIVERY")
    if str(trip.get("status") or "").lower() != "assigned":
        raise CaseFail(f"setup wanted assigned-not-accepted, got {trip.get('status')}")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t = active_trip(oid)
    st = str(t.get("status") or "").lower()
    if st not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"expected auto-accept then OFD, trip={st} pickup={task_status(t,'pickup')}")
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(f"pickup not complete after OFD from assigned: {task_status(t,'pickup')}")
    if not de_busy_on(oid):
        raise CaseFail(f"rider not staying on trip de={ddb_get_de().get('status')}")
    return f"auto-accepted then pickup complete, trip OFD {t.get('trip_id')}"


def tc04() -> str:
    oid = new_oid("04")
    trip = seed_created(oid, "READY_FOR_DELIVERY")
    if trip.get("de_phone"):
        raise CaseFail(f"setup wanted no rider, de={trip.get('de_phone')}")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t = active_trip(oid)
    if str(t.get("status") or "").lower() in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip became OFD with no rider status={t.get('status')}")
    if task_status(t, "pickup").lower() == "completed":
        raise CaseFail("pickup completed with no rider")
    if ddb_get_de().get("current_order_id") == oid:
        raise CaseFail("someone bound to no-rider OFD")
    return "pickup stays open, trip not OFD, nobody freed"


def tc05() -> str:
    oid = new_oid("05")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="OUT_FOR_DELIVERY", store_id=STORE_ID)
    time.sleep(1.2)
    if trips_for(oid, active=True):
        cancel_order(oid)
        stub_put(oid, status="OUT_FOR_DELIVERY", store_id=STORE_ID)
        time.sleep(0.4)
    if trips_for(oid, active=True):
        raise CaseFail(f"setup wanted no trip, got { [t.get('trip_id') for t in trips_for(oid)] }")
    code, parsed, raw = admin_mark(oid, "OUT_FOR_DELIVERY")
    if code != 200:
        raise CaseFail(f"complete-by-order OFD no-trip failed {code} {raw[:200]}")
    if trips_for(oid, active=True):
        raise CaseFail("last-mile created a trip on no-trip OFD")
    return "last-mile no-op, complete-by-order 200, no trip created"


def tc06() -> str:
    oid = new_oid("06")
    seed_created(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t0 = active_trip(oid)
    if task_status(t0, "pickup").lower() == "completed":
        raise CaseFail("pickup completed before later assign")
    set_rider(on_duty=True, free=True)
    code, parsed, raw = admin_assign(oid)
    if code != 200:
        raise CaseFail(f"later assign failed {code} {raw[:200]}")
    time.sleep(0.5)
    t = active_trip(oid)
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(
            f"pickup did not auto-complete after later assign while java already OFD "
            f"pickup={task_status(t,'pickup')} de={t.get('de_phone')}"
        )
    if task_status(t, "drop").lower() == "completed":
        raise CaseFail("drop completed; rider should only have drop left")
    if str(t.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"expected OFD with only drop left, trip={t.get('status')}")
    return "later assign auto-completed pickup; rider has drop only"


def tc07() -> str:
    oid = new_oid("07")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    t0 = active_trip(oid)
    if task_status(t0, "pickup").lower() == "completed":
        raise CaseFail("setup pickup already done")
    since = log_size()
    admin_mark(oid, "DELIVERED")
    t = latest_trip(oid)
    de = ddb_get_de()
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(f"pickup not completed on delivered: {task_status(t,'pickup')}")
    if task_status(t, "drop").lower() != "completed" or str(t.get("status") or "").lower() != "completed":
        raise CaseFail(f"drop/trip not closed status={t.get('status')} drop={task_status(t,'drop')}")
    if str(de.get("status") or "").lower() not in ("free", "eligible") or de.get("current_trip_id"):
        raise CaseFail(f"rider not freed {de.get('status')}")
    pay = trip_pay(t)
    if pay is None or pay <= 0:
        raise CaseFail(f"pay unverifiable total_pay_zmw={pay}")
    if not payment_cod(t):
        raise CaseFail(f"COD missing payment={t.get('payment')}")
    assert_notified(oid, "ORDER_DELIVERED", since)
    return f"pickup then drop, rider free, pay={pay} COD, customer notified"


def tc08() -> str:
    oid = new_oid("08")
    trip = seed_created(oid, "READY_FOR_DELIVERY")
    if trip.get("de_phone"):
        raise CaseFail(f"setup wanted no rider, de={trip.get('de_phone')}")
    admin_mark(oid, "DELIVERED")
    t = latest_trip(oid)
    st = str(t.get("status") or "").lower()
    drop_st = task_status(t, "drop").lower()
    if st != "completed" and drop_st != "completed":
        raise CaseFail(
            f"drop/trip not closed with no rider status={st} drop={drop_st} "
            f"last_sync={_brief(_sync_log[-1] if _sync_log else None)}"
        )
    de = ddb_get_de()
    if de.get("current_order_id") == oid:
        raise CaseFail("rider bound after no-rider delivered")
    return f"trip closed status={st}, nobody to free"


def tc09() -> str:
    oid = new_oid("09")
    trip = seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    ofd0 = len(_ofd_calls)
    sync0 = len(_sync_log)
    code, parsed, raw = complete_pickup_rider(trip)
    if code != 200:
        raise CaseFail(f"rider self pickup failed {code} {raw[:240]}")
    time.sleep(0.3)
    t = active_trip(oid)
    js = stub_get(oid)
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(f"rider pickup not complete {task_status(t,'pickup')}")
    if str(t.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip not OFD after rider pickup {t.get('status')}")
    if str(js.get("status") or "").upper() != "OUT_FOR_DELIVERY":
        raise CaseFail(f"java not OFD after rider pickup {js.get('status')}")
    if len(_sync_log) != sync0:
        raise CaseFail(f"qcom sync ran on rider pickup (loop) extra={_sync_log[sync0:]}")
    # A second pickup complete would 400 INVALID_TASK_TRANSITION; trip still OFD once.
    if not de_busy_on(oid):
        raise CaseFail("rider freed after self pickup")
    return f"rider pickup once, java OFD, no second qcom sync (ofd_calls {len(_ofd_calls)-ofd0})"


def tc10() -> str:
    oid = new_oid("10")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t0 = active_trip(oid)
    if task_status(t0, "pickup").lower() != "completed":
        raise CaseFail("setup pickup not complete")
    code, parsed, raw = admin_mark(oid, "OUT_FOR_DELIVERY")
    if code != 200:
        raise CaseFail(f"second admin OFD broke click {code} {raw[:200]}")
    t = active_trip(oid)
    de = ddb_get_de()
    if str(t.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip left OFD status={t.get('status')}")
    if str(de.get("status") or "").lower() != "busy":
        raise CaseFail(f"rider freed on idempotent OFD {de.get('status')}")
    return "second admin OFD 200 idempotent, trip OFD, rider stays"


def tc11() -> str:
    oid = new_oid("11")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    admin_mark(oid, "DELIVERED")
    t0 = latest_trip(oid)
    tid = str(t0.get("trip_id") or "")
    pay0 = trip_pay(t0)
    lines0 = earnings_for_trip(tid)
    if pay0 is None:
        raise CaseFail("pay unverifiable after first delivered")
    code, parsed, raw = admin_mark(oid, "DELIVERED")
    if code != 200:
        raise CaseFail(f"second admin Delivered broke click {code} {raw[:200]}")
    t = latest_trip(oid)
    pay1 = trip_pay(t)
    lines1 = earnings_for_trip(tid)
    if pay1 is None:
        raise CaseFail("pay unverifiable after second delivered")
    if float(pay1) != float(pay0):
        raise CaseFail(f"second payout total_pay {pay0} -> {pay1}")
    if len(lines1) > len(lines0):
        raise CaseFail(f"second earnings line added {len(lines0)} -> {len(lines1)}")
    if not de_free():
        raise CaseFail(f"rider not staying free {ddb_get_de().get('status')}")
    return f"idempotent delivered, pay stayed {pay1}, no second earnings line"


def tc12() -> str:
    oid = new_oid("12")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    t0 = active_trip(oid)
    code, parsed, raw = admin_mark(oid, "OUT_FOR_DELIVERY", unreachable=True)
    # qcom-only: Java persist is inventory. Unreachable complete-by-order must not change last-mile.
    t = active_trip(oid)
    if task_status(t, "pickup").lower() == "completed" and str(t0.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"last-mile changed while qcom unreachable pickup={task_status(t,'pickup')} trip={t.get('status')}")
    return f"qcom unreachable; last-mile unchanged trip={t.get('status')} code={code}"


def tc13() -> str:
    oid = new_oid("13")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t0 = active_trip(oid)
    code, parsed, raw = admin_mark(oid, "DELIVERED", unreachable=True)
    t = latest_trip(oid)
    if str(t.get("status") or "").lower() == "completed" and str(t0.get("status") or "").lower() != "completed":
        raise CaseFail("drop completed while qcom unreachable")
    return f"qcom unreachable on Delivered; last-mile still {t.get('status')} code={code}"


def tc14() -> str:
    oid = new_oid("14")
    trip = seed_assigned_accepted(oid, "PACKING")
    tid = trip["trip_id"]
    code, parsed, raw = verify_pickup(tid, oid)
    if code != 409 or err_code(parsed) != "ORDER_NOT_PACKED":
        # also try task complete
        c2, p2, r2 = update_task(tid, pickup_task_id(trip), "completed")
        if not (code == 409 and err_code(parsed) == "ORDER_NOT_PACKED") and not (
            c2 == 409 and err_code(p2) == "ORDER_NOT_PACKED"
        ):
            raise CaseFail(
                f"expected 409 ORDER_NOT_PACKED, verify={code} {raw[:160]} task={c2} {_brief(p2)}"
            )
    t = active_trip(oid)
    if str(t.get("status") or "").lower() in ("out_for_delivery", "ofd"):
        raise CaseFail("PACKING pickup incorrectly moved OFD")
    if str(stub_get(oid).get("status") or "").upper() == "OUT_FOR_DELIVERY":
        raise CaseFail("java OFD despite PACKING gate")
    return f"409 ORDER_NOT_PACKED while PACKING; trip={t.get('status')}"


def tc15() -> str:
    oid = new_oid("15")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    sync0 = len(_sync_log)
    code, parsed, raw = non_admin_java_status(oid, "OUT_FOR_DELIVERY")
    if code != 200:
        raise CaseFail(f"non-admin java write {code} {raw[:160]}")
    time.sleep(0.2)
    t = active_trip(oid)
    extra = _sync_log[sync0:]
    if extra:
        raise CaseFail(f"non-admin java status triggered qcom sync {extra}")
    if task_status(t, "pickup").lower() == "completed":
        raise CaseFail("pickup completed on non-admin java OFD")
    if str(t.get("status") or "").lower() in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip OFD on non-admin java write status={t.get('status')}")
    return "DE java status write did not sync last-mile"


def tc16() -> str:
    oid = new_oid("16")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    # admin OFD notify uses empty pickup/complete body — no OTP, no geofence
    admin_mark(oid, "OUT_FOR_DELIVERY")
    t = active_trip(oid)
    if task_status(t, "pickup").lower() != "completed":
        raise CaseFail(
            f"admin OFD required otp/geofence or otherwise failed pickup={task_status(t,'pickup')} "
            f"trip={t.get('status')} sync={_brief(_sync_log[-1] if _sync_log else None)}"
        )
    if str(t.get("status") or "").lower() not in ("out_for_delivery", "ofd"):
        raise CaseFail(f"trip not OFD {t.get('status')}")
    return "admin OFD pickup complete with empty body (no OTP/geofence)"


def tc17() -> str:
    oid = new_oid("17")
    seed_assigned_accepted(oid, "READY_FOR_DELIVERY")
    admin_mark(oid, "OUT_FOR_DELIVERY")
    admin_mark(oid, "DELIVERED")
    t = latest_trip(oid)
    if task_status(t, "drop").lower() != "completed" or str(t.get("status") or "").lower() != "completed":
        raise CaseFail(
            f"admin Delivered required otp/geofence or failed drop={task_status(t,'drop')} "
            f"trip={t.get('status')} sync={_brief(_sync_log[-1] if _sync_log else None)}"
        )
    if not de_free():
        raise CaseFail(f"rider not freed {ddb_get_de().get('status')}")
    return "admin Delivered drop complete with empty body (no OTP/geofence)"


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
    ("TC-16", tc16),
    ("TC-17", tc17),
]


def run_all() -> int:
    global _notify_base
    ensure_health()
    admin_token()
    de_token()
    seed_payout()
    set_rider(on_duty=True, free=True)
    _notify_base = BASE_URL
    stub_is_ours()
    print(
        f"java→qcom path: POST {COMPLETE_BY_ORDER} stub_ours={_stub_ours}",
        flush=True,
    )
    results: list[tuple[str, str, str]] = []
    failed = 0
    for name, fn in CASES:
        before = list(_created)
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
        for oid in _created[len(before) :]:
            _cleanup_case(oid)
        time.sleep(0.2)
    npass = sum(1 for _, s, _ in results if s == "PASS")
    nfail = sum(1 for _, s, _ in results if s == "FAIL")
    nblock = sum(1 for _, s, _ in results if s == "BLOCKED")
    print(f"{len(results)} cases: {npass} PASS, {nfail} FAIL, {nblock} BLOCKED", flush=True)
    return 1 if failed else 0


def main(argv: Optional[list[str]] = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--serve-stubs", action="store_true")
    args = ap.parse_args(argv)
    if args.serve_stubs:
        serve_stubs_forever()
        return 0
    return run_all()


if __name__ == "__main__":
    sys.exit(main())
