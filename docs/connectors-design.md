# Data connectors + SSO — architecture

Goal: support **many data connectors** (Trino, Postgres, MySQL, BigQuery,
Snowflake, ClickHouse, …) instead of being hard-wired to Trino, and let **every
connector authenticate via the user's SSO identity** — regardless of how the user
logged in (Google, Microsoft, generic OIDC, or username/password).

## The core decision: the app is the connector-token issuer

Today Trino trusts the **IdP's** token (Keycloak JWT) and the app passes that
token through. That can't satisfy "any login → any connector", because:

- A **Google** access token is opaque (not a JWT) — Trino can't validate it.
- A **Microsoft** Graph token has the wrong audience.
- A **password** login has no IdP token at all.

So we stop tying connectors to a specific IdP. Instead:

> **Login (any method) → an authenticated app session. For a connector, the app
> mints a short-lived JWT (RS256) describing the user. Connectors trust the
> _app_'s JWKS — one issuer, regardless of login method.**

This decouples *how you log in* from *how a connector authenticates*.

## Three layers

### 1. Identity / login (unchanged — keep all methods)
Google, Microsoft (branded buttons), generic OIDC, username/password. Each one
produces the same thing: an app session with `{username, email}`. Nothing here
knows about connectors.

### 2. Connector registry (replaces Trino-specific wiring)
A declarative registry, single source of truth in the backend, exposed via API.

- **Type** (built-in, code): `trino`, `postgres`, `mysql`, `bigquery`, …
  `{ id, label, icon, driverCoord, driverClass, metaStrategy, defaultAuth }`
- **Instance** (per-deployment, config): an enabled connection
  `{ id, type, label, url, auth }` — built from env (e.g. `TRINO_URL` →
  a `trino` instance, for back-compat; or a `CONNECTORS` JSON list).

Generic consumers (later increments): one kernel helper, one FE catalog browser,
one connect-dialog list — all driven by the registry. Adding a connector =
one registry entry (+ a metadata adapter only for a genuinely new strategy).

### 3. Connector-auth resolver (makes SSO reach every connector)
One endpoint, dispatching by the instance's `auth` strategy:

```
GET /api/v1/connectors/:id/credentials      (called fresh per query)
  app-jwt         → app mints RS256 JWT {sub, preferred_username, email}   ← default
  idp-passthrough → forward the user's real IdP access token (legacy Trino←Keycloak)
  token-exchange  → exchange identity for a cloud cred (BigQuery WIF, AWS STS)
  broker-mapped   → per-user stored cred (Postgres/MySQL that only speak user/pass)
```

`app-jwt` is what unlocks requirement 3: whether the user logged in via Google,
Microsoft, OIDC or password, the app signs its own JWT → JWT-aware connectors
(Trino, Snowflake external-OAuth, …) all validate it against the app's JWKS.

> Honest limits: a source that only understands user/pass (vanilla Postgres) →
> `broker-mapped`; a Google-cloud source that needs a real Google token
> (BigQuery) → `token-exchange`. `app-jwt` can't force a source that doesn't
> accept a JWT — that's a property of the source, not the design.

## What's new in the backbone (this branch)
- **App signing key** (RSA): `CONNECTOR_JWT_PRIVATE_KEY` (PEM) if set, else an
  ephemeral key generated at boot (dev). Private key stays in the backend.
- **JWKS endpoint** `GET /api/v1/.well-known/jwks.json` (public) — connectors
  validate app-minted tokens here.
- **`MintConnectorToken(user)`** — short-lived RS256 JWT (generalizes
  `MintKernelToken`).
- **Connector registry** + `GET /api/v1/connectors` (list for the FE).
- **`GET /api/v1/connectors/:id/credentials`** — the resolver (`app-jwt` +
  `idp-passthrough` to start).
- **`GET /api/v1/connectors/:id/metadata`** — generic browse via a
  `MetadataAdapter` (Trino `SHOW …` adapter is the first; `JDBCInfoSchema` and
  `API` adapters come with their connectors).

Back-compat: `/api/v1/trino/metadata` and `/api/v1/kernel/oidc-token` stay as
aliases; the existing Trino instance keeps `idp-passthrough` (Trino still trusts
Keycloak) so nothing breaks. Switching it to `app-jwt` is a Trino config change
(point its `jwks-url` at the app) done later.

## Trino config when on `app-jwt`
```properties
http-server.authentication.type=JWT
http-server.authentication.jwt.key-file=http://<backend>/api/v1/.well-known/jwks.json
http-server.authentication.jwt.required-issuer=<app issuer>
http-server.authentication.jwt.principal-field=preferred_username
```

## Migration order
1. (this branch) signing key + JWKS + `MintConnectorToken`; registry; generic
   `/connectors` + `/connectors/:id/{credentials,metadata}` with `app-jwt` &
   `idp-passthrough`; keep aliases. Trino instance = `idp-passthrough` (unchanged
   behavior).
2. Kernel: one generic `_read(connector, query)` + aliases generated from the
   registry (keep `trino()`).
3. FE: one generic catalog browser + connector dropdown; connect-dialog + icons
   from the registry.
4. Flip the demo Trino to trust the app (`app-jwt`), proving any-login → Trino.
5. Add a 2nd connector (e.g. Postgres `JDBCInfoSchema` + `broker-mapped`) as a
   registry entry — no new components.

## Adding a connector after the backbone
| Source kind | Work |
|---|---|
| Trino-like | 1 registry entry (reuse trino adapter) |
| JDBC RDBMS (postgres/mysql) | 1 entry + reuse `JDBCInfoSchema` adapter + icon |
| Cloud API (bigquery/snowflake) | 1 entry + 1 API adapter + icon |

UI/kernel/auth layers: unchanged.
