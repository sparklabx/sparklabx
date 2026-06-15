"""SparkLabX Trino helper — auto-loaded into every Python (PySpark) kernel.

Copied into the IPython startup dir by entrypoint.sh, so trino() is available in
every notebook without an import. Reads Trino over JDBC with the logged-in
user's SSO identity (no password): a FRESH OIDC access token is fetched from the
backend per query (in-session refresh), and the refresh token never enters the
kernel.

Kept as a committed file (not a bash heredoc) so it gets normal Python tooling
— linting, formatting, syntax checks. The Scala twin lives in trino.sc.
"""
import os as _os
import time as _time

# Backend route that returns a fresh OIDC access token for this kernel's user.
_OIDC_TOKEN_PATH = "/api/v1/kernel/oidc-token"
_TRINO_DRIVER = "io.trino.jdbc.TrinoDriver"
_HTTP_TIMEOUT_S = 10
_TOKEN_REFRESH_MARGIN_S = 30  # re-fetch this long before the token actually expires

# In-kernel cache of the access token so we hit the backend only when it's stale
# (not on every query). The refresh token stays server-side regardless.
_oidc_cache = {"token": None, "expires_at": 0.0}


def _sparklabx_oidc_token():
    """Return a valid OIDC access token, or None for a non-SSO session.

    No SPARKLABX_API_URL / SPARKLABX_KERNEL_TOKEN in the env → not an SSO
    session → return None and let the caller query without a token. Otherwise
    return a cached token while it's fresh, fetching a new one from the backend
    only when it's (nearly) expired. If this IS an SSO session and we can't get
    a token, RAISE — never silently fall back to an unauthenticated query, which
    would break the "query as yourself" guarantee.
    """
    import json as _json
    import urllib.request as _u
    import urllib.error as _ue
    api = _os.environ.get("SPARKLABX_API_URL")
    tok = _os.environ.get("SPARKLABX_KERNEL_TOKEN")
    if not api or not tok:
        return None  # not an SSO session — fine to query without a token

    # Serve from cache while still comfortably valid.
    if _oidc_cache["token"] and _time.time() < _oidc_cache["expires_at"] - _TOKEN_REFRESH_MARGIN_S:
        return _oidc_cache["token"]

    endpoint = api.rstrip("/") + _OIDC_TOKEN_PATH
    req = _u.Request(endpoint, headers={"Authorization": "Bearer " + tok})
    try:
        with _u.urlopen(req, timeout=_HTTP_TIMEOUT_S) as resp:
            data = _json.loads(resp.read().decode())
    except _ue.HTTPError as e:
        raise RuntimeError(
            f"SparkLabX: OIDC token endpoint returned HTTP {e.code} — your SSO "
            f"session may have expired (re-login)."
        ) from None
    except Exception as e:
        raise RuntimeError(
            f"SparkLabX: could not reach the OIDC token endpoint {endpoint} ({e})."
        ) from None

    if data.get("sso_expired"):
        _oidc_cache["token"] = None
        raise RuntimeError(
            "SparkLabX: your SSO session has expired — please log out and log in again."
        )
    token = data.get("access_token")
    if not token:
        raise RuntimeError(
            "SparkLabX: backend returned no access token for this SSO session."
        )
    expires_in = data.get("expires_in") or 0
    _oidc_cache["token"] = token
    _oidc_cache["expires_at"] = _time.time() + (expires_in if expires_in > 0 else 300)
    return token


def trino(query, url=None):
    """Query Trino with your SSO identity (no password).

    query : a SQL statement ("SELECT ...") or a fully-qualified table name
            "catalog.schema.table".
    url   : jdbc:trino://host:port?SSL=true (defaults to the TRINO_URL env).

    Requires the Trino JDBC driver on the classpath — add the "Trino" connector
    preset when connecting the kernel.
    """
    from pyspark.sql import SparkSession
    spark = SparkSession.builder.getOrCreate()
    u = url or _os.environ.get("TRINO_URL")
    if not u:
        raise ValueError("No Trino URL — pass url=... or set TRINO_URL")
    reader = (spark.read.format("jdbc")
              .option("url", u)
              .option("driver", _TRINO_DRIVER))
    token = _sparklabx_oidc_token()   # fresh per call; raises if SSO + unreachable
    if token:
        reader = reader.option("accessToken", token)
    q = query.strip()
    # A bare table name ("catalog.schema.table") has no whitespace; anything
    # containing whitespace is a SQL statement (SELECT, WITH, leading comment).
    # More robust than a "select" prefix check (handles CTEs / comments).
    if any(ch.isspace() for ch in q):
        reader = reader.option("query", q)
    else:
        reader = reader.option("dbtable", q)
    return reader.load()
