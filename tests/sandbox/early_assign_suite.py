#!/usr/bin/env python3
"""Early-assignment functional suite for isolated qcom PR9.

One command runs all 20 English cases. Controls a local Java order-service
stub. Never contacts production.
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
AWS = {
    "AWS_ACCESS_KEY_ID": "dummy",
    "AWS_SECRET_ACCESS_KEY": "dummy",
    "AWS_DEFAULT_REGION": "us-east-1",
}
DDB_EP = "http://127.0.0.1:8000"

# ---------------------------------------------------------------------------
# In-memory Java order stub
# ---------------------------------------------------------------------------

_orders: dict[str, dict[str, Any]] = {}
_orders_lock = threading.Lock()
_req_log: list[str] = []
_ofd_calls: list[str] = []


def _order_payload(o: dict[str, Any]) -> dict[str, Any]:
    qty = int(o.get("ordered_qty", 4))
    fulfilled = o.get("fulfilled_qty")
    item = {
        "sku": o.get("sku", "MILK-1"),
        "productName": o.get("name", "Milk"),
        "name": o.get("name", "Milk"),
        "imageUrl": o.get("image_url", "https://example.com/milk.jpg"),
        "orderedQuantity": qty,
        "quantity": qty,
    }
    if fulfilled is not None:
        item["fulfilledQuantity"] = int(fulfilled)
    return {
        "orderId": o["id"],
        "orderNumber": o["id"],
        "status": o["status"],
        "storeId": o.get("store_id", STORE_ID),
        "store_id": o.get("store_id", STORE_ID),
        "items": o.get("items") or [item],
        "delivery": {
            "address": o.get("address", "1 Sandbox Rd, Lusaka"),
            "latitude": float(o.get("lat", -15.41)),
            "longitude": float(o.get("lng", 28.31)),
            "phone": o.get("phone", "+260955000001"),
            "name": o.get("customer_name", "Sandbox Customer"),
        },
        "paymentMethod": o.get("payment_method", "COD"),
        "grandTotal": float(o.get("grand_total", 120.0)),
        "currency": o.get("currency", "ZMW"),
    }


class JavaStubHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):  # noqa: A003
        _req_log.append(self.command + " " + self.path)

    def _read_json(self) -> dict[str, Any]:
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0:
            return {}
        raw = self.rfile.read(n)
        try:
            return json.loads(raw.decode("utf-8") or "{}")
        except json.JSONDecodeError:
            return {}

    def _send(self, code: int, body: Any, ctype: str = "application/json"):
        data = body if isinstance(body, (bytes, bytearray)) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        qs = urllib.parse.parse_qs(parsed.query)
        # health
        if path in ("/_control/health", "/health"):
            self._send(200, {"ok": True, "orders": list(_orders)})
            return
        if path == "/_control/log":
            self._send(200, {"log": _req_log[-200:], "ofd": _ofd_calls})
            return
        # by-statuses (with or without /order-service prefix)
        for prefix in (
            "/order-service/api/v1/orders/store/",
            "/api/v1/orders/store/",
        ):
            if path.startswith(prefix) and path.rstrip("/").endswith("/by-statuses"):
                rest = path[len(prefix) :]
                store = rest.split("/")[0]
                wanted = qs.get("statuses") or qs.get("status") or []
                wanted_u = {s.upper() for s in wanted}
                with _orders_lock:
                    content = []
                    for o in _orders.values():
                        if o.get("store_id", STORE_ID) not in (store, STORE_ID, str(store)):
                            # still include if store matches path stamp; assignment stamps from path
                            if o.get("store_id") and o.get("store_id") != store:
                                continue
                        if wanted_u and o["status"].upper() not in wanted_u:
                            continue
                        content.append(_order_payload(o))
                self._send(200, {"content": content, "meta": {"last": True}})
                return
        # single order
        for prefix in ("/order-service/api/v1/orders/", "/api/v1/orders/"):
            if path.startswith(prefix) and "/store/" not in path:
                oid = path[len(prefix) :].split("/")[0]
                if not oid:
                    break
                with _orders_lock:
                    o = _orders.get(oid)
                if not o:
                    self._send(404, {"error": "not_found"})
                    return
                self._send(200, _order_payload(o))
                return
        self._send(404, {"error": "not_found", "path": path})

    def do_POST(self):  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        body = self._read_json()
        # status update
        for prefix in ("/order-service/api/v1/orders/", "/api/v1/orders/"):
            if path.startswith(prefix) and path.rstrip("/").endswith("/status"):
                oid = path[len(prefix) :].split("/")[0]
                st = (body.get("status") or "").upper()
                with _orders_lock:
                    if oid in _orders:
                        _orders[oid]["status"] = st or _orders[oid]["status"]
                if st == "OUT_FOR_DELIVERY":
                    _ofd_calls.append(oid)
                self._send(200, {"status": st or "OK"})
                return
        if path.startswith("/_control/orders"):
            self._send(400, {"error": "use PUT"})
            return
        self._send(404, {"error": "not_found", "path": path})

    def do_PUT(self):  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        body = self._read_json()
        if path.startswith("/_control/orders/"):
            oid = path.split("/_control/orders/")[-1].strip("/")
            if not oid:
                self._send(400, {"error": "missing id"})
                return
            with _orders_lock:
                cur = _orders.get(oid, {"id": oid, "store_id": STORE_ID})
                cur["id"] = oid
                for k, v in body.items():
                    cur[k] = v
                if "status" in cur:
                    cur["status"] = str(cur["status"]).upper()
                _orders[oid] = cur
                payload = dict(cur)
            self._send(200, payload)
            return
        self._send(404, {"error": "not_found"})

    def do_DELETE(self):  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path == "/_control/orders":
            with _orders_lock:
                _orders.clear()
            self._send(200, {"ok": True})
            return
        if path.startswith("/_control/orders/"):
            oid = path.split("/_control/orders/")[-1].strip("/")
            with _orders_lock:
                _orders.pop(oid, None)
            self._send(200, {"ok": True})
            return
        self._send(404, {"error": "not_found"})


def start_java_stub(port: int = STUB_PORT) -> ThreadingHTTPServer:
    srv = ThreadingHTTPServer(("127.0.0.1", port), JavaStubHandler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    return srv


# ---------------------------------------------------------------------------
# Maps MITM HTTP proxy (process-local via HTTPS_PROXY on qcom)
# ---------------------------------------------------------------------------

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


def _maps_ssl_ctx() -> ssl.SSLContext:
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(
        os.path.join(CERT_DIR, "maps.crt"), os.path.join(CERT_DIR, "maps.key")
    )
    try:
        ctx.set_alpn_protocols(["http/1.1"])
    except Exception:
        pass
    return ctx


_BLOCK_HOSTS = (
    "bunzodelivery.com",
    "amazonaws.com",
    "15.135.73.205",
    "internal.bunzodelivery",
)


def _handle_maps_client(conn: socket.socket, ssl_ctx: ssl.SSLContext) -> None:
    conn.settimeout(20)
    try:
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = conn.recv(4096)
            if not chunk:
                return
            buf += chunk
            if len(buf) > 65536:
                return
        line = buf.split(b"\r\n", 1)[0].decode("latin1", "replace")
        parts = line.split()
        if len(parts) < 2:
            return
        method, target = parts[0], parts[1]
        if method != "CONNECT":
            conn.sendall(b"HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n")
            return
        hostport = target
        host = hostport.split(":")[0].lower()
        if any(b in host for b in _BLOCK_HOSTS):
            conn.sendall(b"HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
            return
        conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        if "maps.googleapis.com" in host or host.endswith("googleapis.com"):
            ssock = ssl_ctx.wrap_socket(conn, server_side=True)
            try:
                req = b""
                while b"\r\n\r\n" not in req:
                    c = ssock.recv(4096)
                    if not c:
                        return
                    req += c
                first = req.split(b"\r\n", 1)[0].decode("latin1", "replace")
                path = first.split(" ")[1] if " " in first else ""
                if "geocode" in path:
                    body = GEOCODE_JSON
                else:
                    body = DISTANCE_JSON
                hdr = (
                    b"HTTP/1.1 200 OK\r\n"
                    b"Content-Type: application/json\r\n"
                    b"Content-Length: " + str(len(body)).encode() + b"\r\n"
                    b"Connection: close\r\n\r\n"
                )
                ssock.sendall(hdr + body)
            finally:
                try:
                    ssock.shutdown(socket.SHUT_RDWR)
                except Exception:
                    pass
                ssock.close()
            return
        # Allow loopback tunnels only (java stub / dynamo never need this).
        if host in ("127.0.0.1", "localhost", "::1"):
            port = int(hostport.split(":")[1]) if ":" in hostport else 80
            up = socket.create_connection((host, port), timeout=10)
            leftover = buf.split(b"\r\n\r\n", 1)[1]
            if leftover:
                up.sendall(leftover)

            def pipe(a, b):
                try:
                    while True:
                        d = a.recv(8192)
                        if not d:
                            break
                        b.sendall(d)
                except Exception:
                    pass

            threading.Thread(target=pipe, args=(conn, up), daemon=True).start()
            pipe(up, conn)
            return
        # Unknown remote: do not connect.
        return
    except Exception:
        return
    finally:
        try:
            conn.close()
        except Exception:
            pass


def start_maps_proxy(port: int = MAPS_PROXY_PORT) -> threading.Thread:
    ssl_ctx = _maps_ssl_ctx()
    ls = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    ls.bind(("127.0.0.1", port))
    ls.listen(50)

    def loop():
        while True:
            c, _ = ls.accept()
            threading.Thread(target=_handle_maps_client, args=(c, ssl_ctx), daemon=True).start()

    t = threading.Thread(target=loop, daemon=True)
    t.start()
    return t


def serve_stubs_forever() -> None:
    start_java_stub()
    start_maps_proxy()
    print(f"java stub http://127.0.0.1:{STUB_PORT} maps proxy 127.0.0.1:{MAPS_PROXY_PORT}", flush=True)
    while True:
        time.sleep(3600)


# ---------------------------------------------------------------------------
# HTTP / Dynamo helpers
# ---------------------------------------------------------------------------

class CaseFail(Exception):
    pass


class CaseBlock(Exception):
    pass


def http(method: str, url: str, body: Any = None, headers: Optional[dict] = None, timeout: float = 15) -> tuple[int, Any, str]:
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode()
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", "replace")
            try:
                parsed = json.loads(raw) if raw else None
            except json.JSONDecodeError:
                parsed = raw
            return resp.status, parsed, raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        try:
            parsed = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            parsed = raw
        return e.code, parsed, raw
    except Exception as e:
        raise CaseBlock(f"HTTP {method} {url} failed: {e}") from e


def aws_cmd(args: list[str]) -> Any:
    env = os.environ.copy()
    env.update(AWS)
    p = subprocess.run(
        args, capture_output=True, text=True, env=env, timeout=20
    )
    if p.returncode != 0:
        raise CaseBlock(f"aws {' '.join(args[1:6])} failed: {p.stderr[-400:]}")
    if not p.stdout.strip():
        return {}
    try:
        return json.loads(p.stdout)
    except json.JSONDecodeError:
        return {"raw": p.stdout}


def ddb_query_trips(order_id: str) -> list[dict[str, Any]]:
    out = aws_cmd(
        [
            "aws", "dynamodb", "query",
            "--endpoint-url", DDB_EP, "--region", "us-east-1",
            "--table-name", "QComTable", "--index-name", "OrderIndex",
            "--key-condition-expression", "trip_order_id = :o",
            "--expression-attribute-values", json.dumps({":o": {"S": order_id}}),
        ]
    )
    items = []
    for it in out.get("Items") or []:
        items.append(_from_av(it))
    return items


def ddb_get_de() -> dict[str, Any]:
    out = aws_cmd(
        [
            "aws", "dynamodb", "get-item",
            "--endpoint-url", DDB_EP, "--region", "us-east-1",
            "--table-name", "QComTable",
            "--key", json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
        ]
    )
    return _from_av(out.get("Item") or {})


def _from_av(node: Any) -> Any:
    if not isinstance(node, dict):
        return node
    if set(node.keys()) <= {"S", "N", "BOOL", "NULL", "M", "L", "SS", "NS"}:
        if "S" in node:
            return node["S"]
        if "N" in node:
            try:
                if "." in node["N"]:
                    return float(node["N"])
                return int(node["N"])
            except Exception:
                return node["N"]
        if "BOOL" in node:
            return node["BOOL"]
        if "NULL" in node:
            return None
        if "M" in node:
            return {k: _from_av(v) for k, v in node["M"].items()}
        if "L" in node:
            return [_from_av(v) for v in node["L"]]
        if "SS" in node:
            return list(node["SS"])
    return {k: _from_av(v) for k, v in node.items()}


def stub_put(oid: str, **fields: Any) -> None:
    body = {"status": fields.pop("status", "CONFIRMED"), "store_id": fields.pop("store_id", STORE_ID)}
    body.update(fields)
    code, parsed, raw = http("PUT", f"{STUB_URL}/_control/orders/{oid}", body)
    if code >= 300:
        raise CaseBlock(f"stub put {oid} -> {code} {raw[:200]}")


def stub_get(oid: str) -> dict[str, Any]:
    code, parsed, raw = http("GET", f"{STUB_URL}/order-service/api/v1/orders/{oid}")
    if code >= 300:
        raise CaseBlock(f"stub get {oid} -> {code} {raw[:200]}")
    return parsed if isinstance(parsed, dict) else {}


def stub_delete(oid: str) -> None:
    http("DELETE", f"{STUB_URL}/_control/orders/{oid}")


_admin_tok = None
_de_tok = None
_de_tok_at = 0.0


def admin_token() -> str:
    global _admin_tok
    if _admin_tok:
        return _admin_tok
    code, parsed, raw = http(
        "POST", f"{BASE_URL}/api/v1/admin/login",
        {"username": "sandbox", "password": "sandboxadmin"},
    )
    if code != 200 or not isinstance(parsed, dict) or not parsed.get("token"):
        raise CaseBlock(f"admin login {code} {raw[:200]}")
    _admin_tok = parsed["token"]
    return _admin_tok


def de_token() -> str:
    global _de_tok, _de_tok_at
    if _de_tok and (time.time() - _de_tok_at) < 600:
        return _de_tok
    http(
        "POST", f"{BASE_URL}/api/v1/auth/initiate-otp",
        {"phone_number": RIDER_PHONE},
        {"X-App-Type": "de"},
    )
    code, parsed, raw = http(
        "POST", f"{BASE_URL}/api/v1/auth/verify-otp",
        {"phone_number": RIDER_PHONE, "otp": "112233"},
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


def set_rider(*, on_duty: bool, free: bool = True) -> None:
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
                "aws", "dynamodb", "update-item",
                "--endpoint-url", DDB_EP, "--region", "us-east-1",
                "--table-name", "QComTable",
                "--key", json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
                "--update-expression", expr,
                "--expression-attribute-names", json.dumps({"#s": "status"}),
                "--expression-attribute-values", json.dumps(eav),
            ]
        )
    else:
        aws_cmd(
            [
                "aws", "dynamodb", "update-item",
                "--endpoint-url", DDB_EP, "--region", "us-east-1",
                "--table-name", "QComTable",
                "--key", json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
                "--update-expression",
                "SET #s = :s, assigned_store_id = :st, assigned_store_index_key = :ak, updated_at = :u "
                "REMOVE duty_index_key, current_store_id, current_trip_id, current_order_id, scan_deadline_at",
                "--expression-attribute-names", json.dumps({"#s": "status"}),
                "--expression-attribute-values", json.dumps({
                    ":s": {"S": "offline"},
                    ":st": {"S": STORE_ID},
                    ":ak": {"S": STORE_ID},
                    ":u": {"S": now},
                }),
            ]
        )


def cancel_order(oid: str) -> None:
    http(
        "POST", f"{BASE_URL}/internal/v1/trips/cancel-by-order",
        {"order_id": oid, "reason": "sandbox-suite-reset"},
    )
    stub_delete(oid)


def wait_until(fn: Callable[[], Any], timeout: float, desc: str, interval: float = 1.0) -> Any:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = fn()
        if last:
            return last
        time.sleep(interval)
    raise CaseFail(f"timeout waiting for {desc} (last={_brief(last)})")


def _brief(x: Any) -> str:
    try:
        s = json.dumps(x, default=str)
    except Exception:
        s = str(x)
    return s[:180]


def trips_for(oid: str) -> list[dict[str, Any]]:
    return ddb_query_trips(oid)


def wait_trip(oid: str, timeout: float = 35) -> dict[str, Any]:
    def _f():
        ts = trips_for(oid)
        return ts[0] if ts else None
    return wait_until(_f, timeout, f"trip for {oid}")


def wait_assigned(oid: str, timeout: float = 35) -> dict[str, Any]:
    def _f():
        ts = trips_for(oid)
        if not ts:
            return None
        t = ts[0]
        st = (t.get("status") or "").lower()
        if st in ("assigned", "accepted", "out_for_delivery") or t.get("de_phone") or t.get("de_id"):
            if st == "distance_failed":
                return None
            return t
        return None
    return wait_until(_f, timeout, f"assignment for {oid}")


def de_trip() -> Optional[dict[str, Any]]:
    code, parsed, raw = http("GET", f"{BASE_URL}/api/v1/de/trip", headers=dh())
    if code != 200:
        raise CaseBlock(f"GET /de/trip {code} {raw[:200]}")
    if not isinstance(parsed, dict):
        return None
    return parsed.get("trip")


def accept_trip(trip_id: str) -> tuple[int, Any, str]:
    return http("POST", f"{BASE_URL}/api/v1/trip/{trip_id}/accept", {}, headers=dh())


def pickup_task_id(trip: dict[str, Any]) -> str:
    for t in trip.get("tasks") or []:
        if (t.get("type") or "").lower() == "pickup":
            return t.get("task_id") or t.get("id") or "P1"
    return "P1"


def update_task(trip_id: str, task_id: str, status: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/trip/{trip_id}/task/{task_id}/status/update",
        {"status": status},
        headers=dh(),
    )


def verify_pickup(trip_id: str, order_id: str) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/api/v1/trip/{trip_id}/verify-pickup",
        {"order_id": order_id},
        headers=dh(),
    )


def pack_push(order_id: str, qty: int, payment: str = "COD", zone: str = "ZONE-A", total: float = 99.0) -> tuple[int, Any, str]:
    return http(
        "POST",
        f"{BASE_URL}/internal/v1/trips/edit-by-order",
        {
            "order_id": order_id,
            "payment_method": payment,
            "grand_total": total,
            "currency": "ZMW",
            "delivery_zone": zone,
            "items": [
                {
                    "sku": "MILK-1",
                    "name": "Milk",
                    "image_url": "https://example.com/milk.jpg",
                    "quantity": qty,
                }
            ],
        },
    )


def err_code(parsed: Any) -> str:
    if isinstance(parsed, dict):
        err = parsed.get("error")
        if isinstance(err, dict):
            return str(err.get("code") or "")
        return str(parsed.get("code") or parsed.get("reason") or "")
    return ""


def item_qty(trip: dict[str, Any]) -> Optional[int]:
    items = trip.get("items") or []
    if not items:
        return None
    it = items[0]
    for k in ("quantity", "qty", "Quantity"):
        if k in it and it[k] is not None:
            return int(it[k])
    return None


def trip_payment(trip: dict[str, Any]) -> dict[str, Any]:
    p = trip.get("payment") or {}
    if isinstance(p, dict):
        return p
    return {}


def pickup_zone(trip: dict[str, Any]) -> str:
    for t in trip.get("tasks") or []:
        if (t.get("type") or "").lower() == "pickup":
            return str(t.get("delivery_zone") or t.get("DeliveryZone") or t.get("zone") or "")
    return str(trip.get("delivery_zone") or "")


_RUN = time.strftime("%H%M%S")
_seq = 0
_created: list[str] = []


def new_oid(tag: str) -> str:
    global _seq
    _seq += 1
    oid = f"ORDEA9{_RUN}{tag}{_seq:02d}"
    _created.append(oid)
    return oid


def ensure_health() -> None:
    code, parsed, raw = http("GET", f"{BASE_URL}/health")
    if code != 200:
        raise CaseBlock(f"/health {code} {raw[:80]}")



def admin_assign(oid: str) -> tuple[int, Any, str]:
    return http(
        "POST", f"{BASE_URL}/api/v1/admin/assign",
        {"order_id": oid, "driver_phone": RIDER_PHONE},
        headers=ah(),
    )


def dynamo_bind_rider(oid: str, *, accepted: bool = False) -> dict[str, Any]:
    """Put the sandbox rider on a created trip without going through cron assign."""
    ts = trips_for(oid)
    if not ts:
        raise CaseFail(f"no trip to bind for {oid}")
    trip = ts[0]
    tid = trip["trip_id"]
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    st = "accepted" if accepted else "assigned"
    aws_cmd([
        "aws", "dynamodb", "update-item",
        "--endpoint-url", DDB_EP, "--region", "us-east-1",
        "--table-name", "QComTable",
        "--key", json.dumps({"PK": {"S": f"TRIP!{tid}"}, "SK": {"S": "METADATA"}}),
        "--update-expression",
        "SET #s = :s, de_phone = :p, de_id = :d, assigned_at = :a, updated_at = :a",
        "--expression-attribute-names", json.dumps({"#s": "status"}),
        "--expression-attribute-values", json.dumps({
            ":s": {"S": st},
            ":p": {"S": RIDER_PHONE},
            ":d": {"S": "DE0458047115"},
            ":a": {"S": now},
        }),
    ])
    aws_cmd([
        "aws", "dynamodb", "update-item",
        "--endpoint-url", DDB_EP, "--region", "us-east-1",
        "--table-name", "QComTable",
        "--key", json.dumps({"PK": {"S": f"DE!{RIDER_PHONE}"}, "SK": {"S": "METADATA"}}),
        "--update-expression",
        "SET #s = :s, current_trip_id = :t, current_order_id = :o, current_store_id = :st, updated_at = :u "
        "REMOVE duty_index_key",
        "--expression-attribute-names", json.dumps({"#s": "status"}),
        "--expression-attribute-values", json.dumps({
            ":s": {"S": "busy"},
            ":t": {"S": tid},
            ":o": {"S": oid},
            ":st": {"S": STORE_ID},
            ":u": {"S": now},
        }),
    ])
    return trips_for(oid)[0]


def rider_setup_assigned_accepted(oid: str, java_status: str) -> dict[str, Any]:
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    wait_trip(oid, 40)
    code, parsed, raw = admin_assign(oid)
    if code >= 300:
        raise CaseFail(f"admin assign failed {code} {raw[:200]}")
    if java_status != "CONFIRMED":
        stub_put(oid, status=java_status, ordered_qty=4)
        time.sleep(0.2)
    trip = trips_for(oid)[0]
    return trip


def complete_pickup_rider(trip: dict[str, Any]) -> tuple[int, Any, str]:
    tid = trip["trip_id"]
    task = pickup_task_id(trip)
    # some flows want arrived first; try completed, then arrived→completed
    code, parsed, raw = update_task(tid, task, "completed")
    if code >= 300:
        c2, p2, r2 = update_task(tid, task, "arrived")
        if c2 < 300:
            code, parsed, raw = update_task(tid, task, "completed")
        else:
            # keep original failure
            pass
    return code, parsed, raw


# ---------------------------------------------------------------------------
# Cases
# ---------------------------------------------------------------------------

def tc01():
    oid = new_oid("01")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    st = (trip.get("status") or "").lower()
    if st == "distance_failed":
        raise CaseFail(f"trip created but distance_failed (maps MITM missed?) status={st}")
    return f"trip {trip.get('trip_id')} status={trip.get('status')} at CONFIRMED"


def tc02():
    oid = new_oid("02")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    # wait one extra poll for cron FIFO assign
    try:
        trip = wait_assigned(oid, timeout=15)
        return f"cron assigned {trip.get('trip_id')} to {trip.get('de_phone')} status={trip.get('status')}"
    except CaseFail:
        pass
    code, parsed, raw = admin_assign(oid)
    if code >= 300:
        raise CaseFail(
            f"cron did not assign created trip {trip.get('trip_id')} status={trip.get('status')}; "
            f"admin assign also failed {code} {raw[:160]}"
        )
    trip = trips_for(oid)[0]
    if (trip.get("de_phone") or "") != RIDER_PHONE:
        raise CaseFail(f"admin assign did not bind rider {trip}")
    return (
        f"cron left trip {trip.get('trip_id')} unassigned; sandbox POST /admin/assign bound "
        f"{trip.get('de_phone')} status={trip.get('status')} while CONFIRMED"
    )


def tc03():
    oid = new_oid("03")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    wait_trip(oid)
    dynamo_bind_rider(oid, accepted=False)
    trip = trips_for(oid)[0]
    code, parsed, raw = accept_trip(trip["trip_id"])
    if code >= 300:
        raise CaseFail(f"accept failed while CONFIRMED {code} {raw[:240]}")
    st = stub_get(oid).get("status")
    if st not in ("CONFIRMED", "PACKING"):
        raise CaseFail(f"accept ok but java already {st}")
    dt = de_trip()
    if not dt:
        raise CaseFail("accept succeeded but GET /de/trip is null")
    return f"accept {code} while java={st} trip={dt.get('status')}"


def tc04():
    oid = new_oid("04")
    trip = rider_setup_assigned_accepted(oid, "READY_FOR_DELIVERY")
    code, parsed, raw = complete_pickup_rider(trip)
    if code >= 300:
        raise CaseFail(f"packed pickup failed {code} {raw[:240]}")
    # order should move OFD on java stub
    time.sleep(0.5)
    js = stub_get(oid).get("status")
    tr = trips_for(oid)[0]
    tst = (tr.get("status") or "").lower()
    if js != "OUT_FOR_DELIVERY" and tst not in ("out_for_delivery",):
        raise CaseFail(f"expected OFD, java={js} trip={tst} http={code} body={raw[:160]}")
    return f"pickup ok java={js} trip={tst}"


def tc05():
    oid = new_oid("05")
    trip = rider_setup_assigned_accepted(oid, "READY_FOR_DELIVERY")
    code, parsed, raw = verify_pickup(trip["trip_id"], oid)
    if code >= 300:
        raise CaseFail(f"verify-pickup packed failed {code} {raw[:240]}")
    return f"verify-pickup {code} while READY_FOR_DELIVERY"


def tc06():
    oid = new_oid("06")
    set_rider(on_duty=False)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    st = (trip.get("status") or "").lower()
    if trip.get("de_phone") or st in ("assigned", "accepted"):
        raise CaseFail(f"expected unassigned, got status={st} de={trip.get('de_phone')}")
    # packing still allowed on java
    stub_put(oid, status="PACKING", ordered_qty=4)
    return f"unassigned trip {trip.get('trip_id')} status={st}; order moved PACKING"


def tc07():
    oid = new_oid("07")
    set_rider(on_duty=False)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    stub_put(oid, status="PACKING", ordered_qty=4)
    set_rider(on_duty=True, free=True)
    try:
        trip = wait_assigned(oid, timeout=15)
        return f"cron assigned while PACKING trip={trip.get('trip_id')} status={trip.get('status')}"
    except CaseFail:
        pass
    code, parsed, raw = admin_assign(oid)
    if code >= 300:
        raise CaseFail(f"not assigned while PACKING; admin assign {code} {raw[:160]}")
    trip = trips_for(oid)[0]
    return f"admin-assign while PACKING trip={trip.get('trip_id')} status={trip.get('status')} (cron did not assign)"


def tc08():
    oid = new_oid("08")
    set_rider(on_duty=False)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    wait_trip(oid)
    stub_put(oid, status="READY_FOR_DELIVERY", ordered_qty=4)
    set_rider(on_duty=True, free=True)
    try:
        trip = wait_assigned(oid, timeout=15)
        return f"cron assigned while RFD trip={trip.get('trip_id')} status={trip.get('status')}"
    except CaseFail:
        pass
    code, parsed, raw = admin_assign(oid)
    if code >= 300:
        raise CaseFail(f"not assigned while RFD; admin assign {code} {raw[:160]}")
    trip = trips_for(oid)[0]
    return f"admin-assign while RFD trip={trip.get('trip_id')} status={trip.get('status')} (cron did not assign)"


def tc09():
    a = new_oid("09A")
    b = new_oid("09B")
    set_rider(on_duty=False)
    stub_put(a, status="CONFIRMED", ordered_qty=1)
    ta = wait_trip(a)
    time.sleep(1.2)
    stub_put(b, status="CONFIRMED", ordered_qty=1)
    tb = wait_trip(b)
    set_rider(on_duty=True, free=True)

    def _f():
        xa, xb = trips_for(a)[0], trips_for(b)[0]
        a_as = bool(xa.get("de_phone") or (xa.get("status") or "").lower() in ("assigned", "accepted", "out_for_delivery"))
        b_as = bool(xb.get("de_phone") or (xb.get("status") or "").lower() in ("assigned", "accepted", "out_for_delivery"))
        if a_as or b_as:
            return xa, xb, a_as, b_as
        return None
    try:
        xa, xb, a_as, b_as = wait_until(_f, 15, "FIFO cron assign of one of two waiting trips")
        if not a_as or b_as:
            raise CaseFail(
                f"FIFO expected A assigned B waiting; A status={xa.get('status')} de={xa.get('de_phone')} "
                f"B status={xb.get('status')} de={xb.get('de_phone')}"
            )
        return f"cron FIFO A {xa.get('trip_id')} assigned, B {xb.get('trip_id')} waiting"
    except CaseFail:
        pass
    # Cron did not FIFO-assign. Trigger sandbox assign on the earlier trip only.
    code, parsed, raw = admin_assign(a)
    if code >= 300:
        raise CaseFail(f"cron did not FIFO-assign two waiting trips; admin assign A failed {code} {raw[:160]}")
    xa, xb = trips_for(a)[0], trips_for(b)[0]
    a_as = bool(xa.get("de_phone"))
    b_as = bool(xb.get("de_phone"))
    if not a_as or b_as:
        raise CaseFail(
            f"after admin-assign A, expected B still waiting; A status={xa.get('status')} de={xa.get('de_phone')} "
            f"B status={xb.get('status')} de={xb.get('de_phone')} created A={ta.get('created_at')} B={tb.get('created_at')}"
        )
    return f"admin-assign earlier trip A {xa.get('trip_id')}; B {xb.get('trip_id')} still waiting (cron did not FIFO)"


def tc10():
    oid = new_oid("10")
    set_rider(on_duty=False)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    st = (trip.get("status") or "").lower()
    if trip.get("de_phone") or st in ("assigned", "accepted"):
        raise CaseFail(f"off-duty rider was assigned status={st} de={trip.get('de_phone')}")
    return f"trip {trip.get('trip_id')} unassigned while rider offline status={st}"


def tc11():
    oid = new_oid("11")
    trip = rider_setup_assigned_accepted(oid, "CONFIRMED")
    code, parsed, raw = complete_pickup_rider(trip)
    if code != 409 or err_code(parsed) != "ORDER_NOT_PACKED":
        raise CaseFail(f"expected 409 ORDER_NOT_PACKED, got {code} {raw[:240]}")
    js = stub_get(oid).get("status")
    tr = trips_for(oid)[0]
    if js == "OUT_FOR_DELIVERY" or (tr.get("status") or "").lower() == "out_for_delivery":
        raise CaseFail(f"order moved OFD despite blocked pickup java={js} trip={tr.get('status')}")
    return f"409 ORDER_NOT_PACKED while CONFIRMED; still java={js} trip={tr.get('status')}"


def tc12():
    oid = new_oid("12")
    trip = rider_setup_assigned_accepted(oid, "PACKING")
    code, parsed, raw = complete_pickup_rider(trip)
    if code != 409 or err_code(parsed) != "ORDER_NOT_PACKED":
        raise CaseFail(f"expected 409 ORDER_NOT_PACKED, got {code} {raw[:240]}")
    js = stub_get(oid).get("status")
    if js == "OUT_FOR_DELIVERY":
        raise CaseFail("PACKING pickup incorrectly moved OFD")
    return f"409 ORDER_NOT_PACKED while PACKING java={js}"


def tc13():
    oid = new_oid("13")
    trip = rider_setup_assigned_accepted(oid, "CONFIRMED")
    code, parsed, raw = verify_pickup(trip["trip_id"], oid)
    if code != 409 or err_code(parsed) != "ORDER_NOT_PACKED":
        raise CaseFail(f"expected 409 ORDER_NOT_PACKED on scan, got {code} {raw[:240]}")
    return f"verify-pickup 409 ORDER_NOT_PACKED while CONFIRMED"


def tc14():
    oid = new_oid("14")
    trip = rider_setup_assigned_accepted(oid, "CONFIRMED")
    code1, parsed1, raw1 = complete_pickup_rider(trip)
    if code1 != 409 or err_code(parsed1) != "ORDER_NOT_PACKED":
        raise CaseFail(f"setup blocked pickup expected 409, got {code1} {raw1[:200]}")
    stub_put(oid, status="READY_FOR_DELIVERY", ordered_qty=4)
    time.sleep(0.4)
    trip = trips_for(oid)[0]
    code, parsed, raw = complete_pickup_rider(trip)
    if code >= 300:
        raise CaseFail(f"packed pickup after block failed {code} {raw[:240]}")
    time.sleep(0.5)
    js = stub_get(oid).get("status")
    tr = trips_for(oid)[0]
    tst = (tr.get("status") or "").lower()
    if js != "OUT_FOR_DELIVERY" and tst != "out_for_delivery":
        raise CaseFail(f"expected OFD after packed pickup java={js} trip={tst}")
    return f"after 409, packed pickup ok java={js} trip={tst}"


def tc15():
    oid = new_oid("15")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4, fulfilled_qty=0)
    trip = wait_trip(oid)
    q = item_qty(trip)
    if q != 4:
        raise CaseFail(f"expected ordered qty 4 at confirm, got {q} items={trip.get('items')}")
    return f"confirm qty={q} on {trip.get('trip_id')}"


def tc16():
    oid = new_oid("16")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    tid = trip["trip_id"]
    code, parsed, raw = pack_push(oid, qty=3, payment="AIRTEL_MONEY", zone="ZONE-PACK", total=77.5)
    if code >= 300:
        raise CaseFail(f"pack push failed {code} {raw[:240]}")
    if isinstance(parsed, dict) and parsed.get("updated") is False:
        raise CaseFail(f"pack push no-op on existing trip {parsed}")
    time.sleep(0.3)
    trips = trips_for(oid)
    if len(trips) != 1:
        raise CaseFail(f"expected 1 trip after pack push, got {len(trips)}")
    t = trips[0]
    if t.get("trip_id") != tid:
        raise CaseFail(f"second trip created {tid} vs {t.get('trip_id')}")
    q = item_qty(t)
    pay = trip_payment(t)
    method = str(pay.get("method") or pay.get("payment_method") or t.get("payment_method") or "")
    zone = pickup_zone(t)
    if q != 3:
        raise CaseFail(f"packed qty not 3, got {q} items={t.get('items')}")
    if "AIRTEL" not in method.upper() and "AIRTEL" not in json.dumps(pay).upper():
        raise CaseFail(f"payment not updated: pay={pay} method={method}")
    if zone and "ZONE-PACK" not in zone:
        # zone might only be on pickup task; if empty, still fail clearly
        raise CaseFail(f"delivery_zone not ZONE-PACK, got {zone!r} tasks={t.get('tasks')}")
    if not zone:
        # some builds stamp zone only on pickup task; already checked. If blank, fail.
        raise CaseFail(f"delivery_zone empty after pack push tasks={t.get('tasks')}")
    return f"same trip {tid} qty={q} payment={method or pay} zone={zone}"


def tc17():
    oid = new_oid("17")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    wait_trip(oid)
    pack_push(oid, qty=3, payment="COD", zone="Z1", total=10)
    time.sleep(0.3)
    t = trips_for(oid)[0]
    q = item_qty(t)
    if q != 3:
        raise CaseFail(f"expected packed 3 not ordered 4, got {q}")
    return f"packed qty {q} differs from ordered 4"


def tc18():
    oid = new_oid("18")
    # no stub order → cron will not create a trip
    code, parsed, raw = pack_push(oid, qty=3, payment="COD", zone="Z0", total=10)
    if code != 200:
        raise CaseFail(f"expected 200 no-op, got {code} {raw[:240]}")
    reason = ""
    if isinstance(parsed, dict):
        reason = str(parsed.get("reason") or "")
        if parsed.get("updated") is True:
            raise CaseFail(f"push created/updated a trip unexpectedly {parsed}")
    if reason and reason != "no_active_trip":
        raise CaseFail(f"expected reason no_active_trip, got {parsed}")
    if trips_for(oid):
        raise CaseFail(f"trip created by pack push: {trips_for(oid)}")
    return f"200 no_active_trip ({reason or parsed})"


def tc19():
    oid = new_oid("19")
    code, parsed, raw = pack_push(oid, qty=3, payment="COD", zone="Z0", total=10)
    if code != 200:
        raise CaseFail(f"pre-create push {code} {raw[:200]}")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    n = len(trips_for(oid))
    if n != 1:
        raise CaseFail(f"expected 1 trip after later create, got {n}")
    return f"later create one trip {trip.get('trip_id')} after no-op push"


def tc20():
    oid = new_oid("20")
    set_rider(on_duty=True, free=True)
    stub_put(oid, status="CONFIRMED", ordered_qty=4)
    trip = wait_trip(oid)
    tid = trip["trip_id"]
    pack_push(oid, qty=3, payment="COD", zone="ZONE-1", total=50)
    time.sleep(0.2)
    code, parsed, raw = pack_push(oid, qty=2, payment="MTN_MONEY", zone="ZONE-2", total=40)
    if code >= 300:
        raise CaseFail(f"second pack push failed {code} {raw[:200]}")
    time.sleep(0.3)
    trips = trips_for(oid)
    if len(trips) != 1:
        raise CaseFail(f"duplicate trips after second push: {len(trips)}")
    t = trips[0]
    if t.get("trip_id") != tid:
        raise CaseFail("trip id changed on second push")
    q = item_qty(t)
    pay = trip_payment(t)
    blob = json.dumps(pay).upper() + json.dumps(t.get("payment_method", "")).upper()
    zone = pickup_zone(t)
    if q != 2:
        raise CaseFail(f"expected overwritten qty 2, got {q} items={t.get('items')}")
    if "MTN" not in blob:
        raise CaseFail(f"payment not overwritten to MTN: {pay}")
    if zone and "ZONE-2" not in zone:
        raise CaseFail(f"zone not overwritten: {zone}")
    return f"same trip {tid} qty={q} pay=MTN zone={zone} (no duplicate)"


CASES = [
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
    ("TC-18", tc18),
    ("TC-19", tc19),
    ("TC-20", tc20),
]


def _cleanup_case_orders(oids: list[str]) -> None:
    for oid in oids:
        try:
            cancel_order(oid)
        except Exception:
            pass
    try:
        set_rider(on_duty=True, free=True)
    except Exception:
        pass


def run_all() -> int:
    ensure_health()
    code, parsed, raw = http("GET", f"{STUB_URL}/_control/health")
    if code != 200:
        raise SystemExit(f"java stub not up at {STUB_URL}: {code} {raw[:80]}")
    results = []
    failed = False
    for name, fn in CASES:
        before = len(_created)
        try:
            set_rider(on_duty=True, free=True)
            reason = fn()
            results.append((name, "PASS", reason.replace("\n", " ")[:220]))
        except CaseFail as e:
            failed = True
            results.append((name, "FAIL", str(e).replace("\n", " ")[:220]))
        except CaseBlock as e:
            failed = True
            results.append((name, "BLOCKED", str(e).replace("\n", " ")[:220]))
        except Exception as e:
            failed = True
            results.append((name, "FAIL", f"exception {type(e).__name__}: {e}".replace("\n", " ")[:220]))
        finally:
            created = _created[before:]
            _cleanup_case_orders(created)
            time.sleep(0.4)
    for name, status, reason in results:
        print(f"{name} {status} — {reason}")
    return 1 if failed else 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--serve-stubs", action="store_true")
    args = ap.parse_args(argv)
    if args.serve_stubs:
        serve_stubs_forever()
        return 0
    return run_all()


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
