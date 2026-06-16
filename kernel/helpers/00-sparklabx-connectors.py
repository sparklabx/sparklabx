"""SparkLabX data connectors — auto-loaded into every Python (PySpark) kernel.

The backend injects the configured connectors (SPARKLABX_CONNECTORS). For each
one a helper named after the connector is created, e.g.:

    trino("catalog.schema.table")     trino("SELECT ...")
    postgres("public.users")          postgres("SELECT ...")

or use the generic form: query("trino", "SELECT ...").

Each call fetches a FRESH per-query credential from the backend tied to the
user's SSO identity (the app mints/forwards the right token) — no passwords in
the notebook, and connectors authenticate as the logged-in user.
"""
import os as _os
import json as _json
import time as _time

_API = _os.environ.get("SPARKLABX_API_URL")
_KTOKEN = _os.environ.get("SPARKLABX_KERNEL_TOKEN")
_CRED_PATH = "/api/v1/connectors/{id}/credentials"
_HTTP_TIMEOUT_S = 10
_REFRESH_MARGIN_S = 30

# id -> {id, driver, url}
_CONNECTORS = {}
try:
    for _c in _json.loads(_os.environ.get("SPARKLABX_CONNECTORS") or "[]"):
        _CONNECTORS[_c["id"]] = _c
except Exception:
    pass
# Back-compat: no manifest but a default TRINO_URL → synthesize a trino connector.
if not _CONNECTORS and _os.environ.get("TRINO_URL"):
    _CONNECTORS["trino"] = {"id": "trino", "kind": "trino", "driver": "io.trino.jdbc.TrinoDriver", "url": _os.environ["TRINO_URL"]}

_cred_cache = {}  # id -> {scheme, token, user, password, exp}


def _credential(cid):
    """Fresh credential for a connector (cached until near expiry). Returns None
    for a non-SSO session; raises with a clear message if the SSO session has
    expired or the backend is unreachable (never silently query unauthenticated)."""
    import urllib.request as _u
    import urllib.error as _ue
    c = _cred_cache.get(cid)
    if c and _time.time() < c["exp"] - _REFRESH_MARGIN_S:
        return c
    if not _API or not _KTOKEN:
        return None  # not an SSO session — query without a credential
    endpoint = _API.rstrip("/") + _CRED_PATH.format(id=cid)
    req = _u.Request(endpoint, headers={"Authorization": "Bearer " + _KTOKEN})
    try:
        with _u.urlopen(req, timeout=_HTTP_TIMEOUT_S) as resp:
            d = _json.loads(resp.read().decode())
    except _ue.HTTPError as e:
        raise RuntimeError(
            f"SparkLabX: credential endpoint returned HTTP {e.code} for '{cid}' — "
            f"your SSO session may have expired (re-login)."
        ) from None
    except Exception as e:
        raise RuntimeError(f"SparkLabX: cannot reach the credential endpoint for '{cid}' ({e}).") from None
    if d.get("sso_expired"):
        _cred_cache.pop(cid, None)
        raise RuntimeError("SparkLabX: your SSO session has expired — please log out and log in again.")
    expires_in = d.get("expires_in") or 0
    cred = {
        "scheme": d.get("scheme", "bearer"),
        "token": d.get("access_token") or "",
        "user": d.get("username") or "",
        "password": d.get("password") or "",
        "exp": _time.time() + (expires_in if expires_in > 0 else 300),
    }
    _cred_cache[cid] = cred
    return cred


def query(connector, sql, url=None):
    """Run SQL (or read a fully-qualified table) on a connector, as your SSO
    identity. `connector` is a connector id (e.g. "trino")."""
    from pyspark.sql import SparkSession
    spark = SparkSession.builder.getOrCreate()
    conn = _CONNECTORS.get(connector)
    if conn is None and url is None:
        raise ValueError(f"SparkLabX: unknown connector '{connector}' (configured: {list(_CONNECTORS) or 'none'})")
    u = url or conn["url"]
    driver = (conn or {}).get("driver", "io.trino.jdbc.TrinoDriver")
    reader = spark.read.format("jdbc").option("url", u).option("driver", driver)
    cred = _credential(connector)
    if cred:
        if cred["scheme"] == "user-password" and cred["user"]:
            reader = reader.option("user", cred["user"]).option("password", cred["password"])
        elif cred["token"]:
            reader = reader.option("accessToken", cred["token"])
    q = sql.strip()
    # Bare "catalog.schema.table" has no whitespace; whitespace ⇒ a SQL statement.
    if any(ch.isspace() for ch in q):
        reader = reader.option("query", q)
    else:
        reader = reader.option("dbtable", q)
    return reader.load()


def _make_alias(cid):
    def fn(sql, url=None):
        return query(cid, sql, url=url)
    fn.__name__ = cid
    fn.__doc__ = f"Query the '{cid}' connector as your SSO identity. {cid}('catalog.schema.table') or {cid}('SELECT ...')."
    return fn


def _make_ambiguous(kind, ids):
    def fn(*_args, **_kwargs):
        raise RuntimeError(
            f"SparkLabX: there are several '{kind}' connectors ({', '.join(ids)}) — "
            f"call one by id, e.g. {ids[0]}('schema.table'), or query('{ids[0]}', 'SELECT ...').")
    fn.__name__ = kind
    return fn


# Per-connector helper named after its id (e.g. trino, analytics_pg).
for _cid in _CONNECTORS:
    globals()[_cid] = _make_alias(_cid)

# Type helpers (postgres(), mysql(), trino()): when a kind has exactly one
# connector, call it by the kind name too — the common, tidy case. With several
# of a kind, the type name raises a helpful "call it by id" error instead.
_by_kind = {}
for _c in _CONNECTORS.values():
    _by_kind.setdefault(_c.get("kind") or "", []).append(_c["id"])
for _kind, _ids in _by_kind.items():
    if not _kind or _kind in _CONNECTORS:
        continue  # no kind, or an instance already owns that exact name
    globals()[_kind] = _make_alias(_ids[0]) if len(_ids) == 1 else _make_ambiguous(_kind, _ids)

# trino() is always defined for back-compat even before a manifest is injected.
if "trino" not in globals():
    globals()["trino"] = _make_alias("trino")
