// SparkLabX: trino() — query Trino with your SSO identity (no password).
//
// Appended to the Almond predef.sc by entrypoint.sh (as a marked block so it can
// be re-applied/updated safely). Scala twin of helpers/00-sparklabx-trino.py.
// Fetches a FRESH OIDC token from the backend per call (in-session refresh); the
// refresh token never enters the kernel. Requires the Trino JDBC driver on the
// classpath (add the "Trino" connector preset).
// In-kernel cache of the access token so we hit the backend only when it's
// stale, not on every query. The refresh token stays server-side regardless.
var _trinoToken: String = null
var _trinoTokenExpiresAt: Long = 0L

def _sparklabxOidcToken(): String = {
  // Non-SSO session (no backend env) → null, caller queries without a token.
  // SSO session but fetch fails → throw; never silently query unauthenticated.
  val api = sys.env.get("SPARKLABX_API_URL")
  val kt = sys.env.get("SPARKLABX_KERNEL_TOKEN")
  if (api.isEmpty || kt.isEmpty) return null
  val now = System.currentTimeMillis() / 1000
  if (_trinoToken != null && now < _trinoTokenExpiresAt - 30) return _trinoToken

  val endpoint = api.get.stripSuffix("/") + "/api/v1/kernel/oidc-token"
  val conn = new java.net.URL(endpoint).openConnection().asInstanceOf[java.net.HttpURLConnection]
  conn.setRequestProperty("Authorization", "Bearer " + kt.get)
  conn.setConnectTimeout(10000); conn.setReadTimeout(10000)
  try {
    val code = conn.getResponseCode
    if (code != 200)
      throw new RuntimeException(
        s"SparkLabX: OIDC token endpoint returned HTTP $code — your SSO session may have expired (re-login).")
    val body = scala.io.Source.fromInputStream(conn.getInputStream, "UTF-8").mkString
    // Real JSON parse (Jackson ships with Spark) — robust vs a regex on a token.
    val node = new com.fasterxml.jackson.databind.ObjectMapper().readTree(body)
    if (node.path("sso_expired").asBoolean(false)) {
      _trinoToken = null
      throw new RuntimeException(
        "SparkLabX: your SSO session has expired — please log out and log in again.")
    }
    val token = node.path("access_token").asText("")
    if (token.isEmpty)
      throw new RuntimeException("SparkLabX: backend returned no access token for this SSO session.")
    val expiresIn = node.path("expires_in").asInt(0)
    _trinoToken = token
    _trinoTokenExpiresAt = now + (if (expiresIn > 0) expiresIn else 300).toLong
    token
  } catch {
    case e: java.io.IOException =>
      throw new RuntimeException(
        s"SparkLabX: could not reach the OIDC token endpoint $endpoint (${e.getMessage})", e)
  } finally {
    conn.disconnect()
  }
}

def trino(query: String, url: String = null): org.apache.spark.sql.DataFrame = {
  val u = Option(url).getOrElse(sys.env.getOrElse("TRINO_URL",
    throw new RuntimeException("No Trino URL — pass url=... or set TRINO_URL")))
  val token = _sparklabxOidcToken()  // fresh per call; throws if SSO + unreachable
  var r = spark.read.format("jdbc").option("url", u).option("driver", "io.trino.jdbc.TrinoDriver")
  if (token != null) r = r.option("accessToken", token)
  val q = query.trim
  // Bare table name has no whitespace; whitespace ⇒ SQL statement (see Python twin).
  r = if (q.exists(_.isWhitespace)) r.option("query", q) else r.option("dbtable", q)
  r.load()
}
