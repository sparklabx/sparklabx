// SparkLabX: data connectors for Scala notebooks.
//   query("trino", "SELECT ...")   or the trino(...) alias.
// Each call fetches a FRESH per-query credential from the backend tied to your
// SSO identity (no passwords in the notebook). Python twin: 00-sparklabx-connectors.py.

import scala.collection.mutable

val _spxMapper = new com.fasterxml.jackson.databind.ObjectMapper()
val _spxApi = sys.env.get("SPARKLABX_API_URL")
val _spxKtoken = sys.env.get("SPARKLABX_KERNEL_TOKEN")

// connector id -> (driverClass, url)
val _spxConnectors: Map[String, (String, String)] = {
  val m = mutable.Map[String, (String, String)]()
  sys.env.get("SPARKLABX_CONNECTORS").foreach { raw =>
    try {
      val it = _spxMapper.readTree(raw).elements()
      while (it.hasNext) {
        val n = it.next()
        m(n.path("id").asText("")) = (n.path("driver").asText("io.trino.jdbc.TrinoDriver"), n.path("url").asText(""))
      }
    } catch { case _: Throwable => }
  }
  m.toMap
}

// connector id -> kind (trino|postgres|mysql), for the type helpers below.
val _spxKinds: Map[String, String] = {
  val m = mutable.Map[String, String]()
  sys.env.get("SPARKLABX_CONNECTORS").foreach { raw =>
    try {
      val it = _spxMapper.readTree(raw).elements()
      while (it.hasNext) {
        val n = it.next()
        m(n.path("id").asText("")) = n.path("kind").asText("")
      }
    } catch { case _: Throwable => }
  }
  m.toMap
}

// The single connector id of a given kind, or a clear error when none / several.
def _spxKindId(kind: String): String = {
  _spxKinds.collect { case (id, k) if k == kind => id }.toList match {
    case one :: Nil => one
    case Nil        => throw new RuntimeException(s"SparkLabX: no '$kind' connector configured")
    case many       => throw new RuntimeException(
      s"""SparkLabX: several '$kind' connectors (${many.mkString(", ")}) - call one by id: query("${many.head}", ...)""")
  }
}

// connector id -> (scheme, token, user, password, expiresAtEpochSec)
val _spxCredCache = mutable.Map[String, (String, String, String, String, Long)]()

def _spxCredential(cid: String): Option[(String, String, String, String)] = {
  val api = _spxApi.getOrElse(return None)
  val kt = _spxKtoken.getOrElse(return None)
  val now = System.currentTimeMillis() / 1000
  _spxCredCache.get(cid) match {
    case Some((sc, tok, u, p, exp)) if now < exp - 30 => return Some((sc, tok, u, p))
    case _ =>
  }
  val endpoint = api.stripSuffix("/") + "/api/v1/connectors/" + cid + "/credentials"
  val conn = new java.net.URL(endpoint).openConnection().asInstanceOf[java.net.HttpURLConnection]
  conn.setRequestProperty("Authorization", "Bearer " + kt)
  conn.setConnectTimeout(10000); conn.setReadTimeout(10000)
  try {
    val code = conn.getResponseCode
    if (code != 200)
      throw new RuntimeException(
        s"SparkLabX: credential endpoint HTTP $code for '$cid' — your SSO session may have expired (re-login).")
    val body = scala.io.Source.fromInputStream(conn.getInputStream, "UTF-8").mkString
    val node = _spxMapper.readTree(body)
    if (node.path("sso_expired").asBoolean(false)) {
      _spxCredCache.remove(cid)
      throw new RuntimeException("SparkLabX: your SSO session has expired — please log out and log in again.")
    }
    val sc = node.path("scheme").asText("bearer")
    val tok = node.path("access_token").asText("")
    val u = node.path("username").asText("")
    val p = node.path("password").asText("")
    val expIn = node.path("expires_in").asInt(0)
    _spxCredCache(cid) = (sc, tok, u, p, now + (if (expIn > 0) expIn else 300).toLong)
    Some((sc, tok, u, p))
  } catch {
    case e: java.io.IOException =>
      throw new RuntimeException(s"SparkLabX: cannot reach the credential endpoint for '$cid' (${e.getMessage})", e)
  } finally {
    conn.disconnect()
  }
}

def query(connector: String, sql: String, url: String = null): org.apache.spark.sql.DataFrame = {
  val conn = _spxConnectors.get(connector)
  val u = Option(url).orElse(conn.map(_._2)).getOrElse(
    throw new RuntimeException(s"SparkLabX: unknown connector '$connector'"))
  val driver = conn.map(_._1).getOrElse("io.trino.jdbc.TrinoDriver")
  var r = spark.read.format("jdbc").option("url", u).option("driver", driver)
  _spxCredential(connector).foreach { case (scheme, tok, user, pass) =>
    if (scheme == "user-password" && user.nonEmpty) r = r.option("user", user).option("password", pass)
    else if (tok.nonEmpty) r = r.option("accessToken", tok)
  }
  val q = sql.trim
  r = if (q.exists(_.isWhitespace)) r.option("query", q) else r.option("dbtable", q)
  r.load()
}

// Type helpers — call the sole connector of a kind by its type name (the tidy
// common case). With several of a kind these throw "call it by id". Scala can't
// define a top-level def per connector id dynamically, so use query("id", ...)
// for a specific instance among many.
def trino(sql: String, url: String = null): org.apache.spark.sql.DataFrame = query("trino", sql, url)
def postgres(sql: String, url: String = null): org.apache.spark.sql.DataFrame = query(_spxKindId("postgres"), sql, url)
def mysql(sql: String, url: String = null): org.apache.spark.sql.DataFrame = query(_spxKindId("mysql"), sql, url)
