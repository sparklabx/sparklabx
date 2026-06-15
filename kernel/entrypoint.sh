#!/bin/bash
set -e

echo "Starting Jupyter Kernel Gateway for student: ${STUDENT_ID:-unknown}"

unset JAVA_TOOL_OPTIONS

CONF_DIR="${SPARK_CONF_DIR:-/home/exam/spark-conf}"
S3A_JAR_DIR="/usr/local/lib/python3.11/dist-packages/pyspark/jars"
SCALA_KERNEL_JSON="/root/.local/share/jupyter/kernels/scala212/kernel.json"

mkdir -p "$CONF_DIR"

# Credential provider selection
#   S3_ENDPOINT set → MinIO/RustFS: SimpleAWSCredentialsProvider (no session token)
#   S3_ENDPOINT empty → AWS S3 cloud: TemporaryAWSCredentialsProvider (STS session token)
if [ -n "$S3_ENDPOINT" ]; then
  MODE="minio"
  PROVIDER="org.apache.hadoop.fs.s3a.SimpleAWSCredentialsProvider"
else
  MODE="cloud"
  PROVIDER="org.apache.hadoop.fs.s3a.TemporaryAWSCredentialsProvider"
fi
echo "S3A mode: $MODE (provider: $PROVIDER)"

# ── 1. core-site.xml (Hadoop conf, read via HADOOP_CONF_DIR) ───────────────────
if [ "$MODE" = "minio" ]; then
  cat > "$CONF_DIR/core-site.xml" <<XML
<?xml version="1.0" encoding="UTF-8"?>
<configuration>
  <property><name>fs.s3a.impl</name><value>org.apache.hadoop.fs.s3a.S3AFileSystem</value></property>
  <property><name>fs.s3a.path.style.access</name><value>true</value></property>
  <property><name>fs.s3a.endpoint</name><value>${S3_ENDPOINT}</value></property>
  <property><name>fs.s3a.aws.credentials.provider</name><value>${PROVIDER}</value></property>
  <property><name>fs.s3a.access.key</name><value>${AWS_ACCESS_KEY_ID:-minioadmin}</value></property>
  <property><name>fs.s3a.secret.key</name><value>${AWS_SECRET_ACCESS_KEY:-minioadmin}</value></property>
  <!-- Keep directory markers so S3A doesn't try to delete parent-prefix markers
       (e.g. "users/") on overwrite — those are outside a user's IAM scope
       (users/<slug>/*) and the delete is denied, producing noisy (harmless)
       AccessDenied warnings. "keep" also avoids extra delete calls on writes. -->
  <property><name>fs.s3a.directory.marker.retention</name><value>keep</value></property>
</configuration>
XML
  echo "core-site.xml → MinIO (endpoint: $S3_ENDPOINT)"
fi

# ── 2. spark-defaults.conf ────────────────────────────────────────────────────
SPARK_DEFAULTS="$CONF_DIR/spark-defaults.conf"
{
  echo "spark.hadoop.fs.s3a.impl org.apache.hadoop.fs.s3a.S3AFileSystem"
  echo "spark.hadoop.fs.s3a.path.style.access true"
  echo "spark.hadoop.fs.s3a.aws.credentials.provider $PROVIDER"
  if [ "$MODE" = "minio" ]; then
    echo "spark.hadoop.fs.s3a.endpoint $S3_ENDPOINT"
    echo "spark.hadoop.fs.s3a.access.key ${AWS_ACCESS_KEY_ID:-minioadmin}"
    echo "spark.hadoop.fs.s3a.secret.key ${AWS_SECRET_ACCESS_KEY:-minioadmin}"
  fi
  # Don't delete parent-prefix directory markers (e.g. "users/") on overwrite —
  # outside the user's IAM scope, so the delete is denied (harmless but noisy).
  echo "spark.hadoop.fs.s3a.directory.marker.retention keep"
} > "$SPARK_DEFAULTS"
echo "spark-defaults.conf written"

# ── 3. Patch Almond Scala kernel.json: JAR classpath + JVM -D props ───────────
if [ -f "$S3A_JAR_DIR/hadoop-aws-3.3.4.jar" ] && [ -f "$SCALA_KERNEL_JSON" ]; then
  MODE="$MODE" PROVIDER="$PROVIDER" python3 - <<'PYEOF'
import json, os

kernel_path = "/root/.local/share/jupyter/kernels/scala212/kernel.json"
jar_dir = "/usr/local/lib/python3.11/dist-packages/pyspark/jars"
conf_dir = os.environ.get("SPARK_CONF_DIR", "/home/exam/spark-conf")
mode = os.environ["MODE"]
provider = os.environ["PROVIDER"]
s3_endpoint = os.environ.get("S3_ENDPOINT", "")
access_key = os.environ.get("AWS_ACCESS_KEY_ID", "minioadmin")
secret_key = os.environ.get("AWS_SECRET_ACCESS_KEY", "minioadmin")

with open(kernel_path) as f:
    k = json.load(f)
argv = k["argv"]

# Idempotent JAR injection
cp_idx = argv.index("-cp")
cp_str = argv[cp_idx + 1]
jars_to_add = [
    f"{jar_dir}/hadoop-aws-3.3.4.jar",
    f"{jar_dir}/aws-java-sdk-bundle-1.12.262.jar",
]
existing = set(cp_str.split(":"))
new_jars = [j for j in jars_to_add if j not in existing]
if new_jars:
    argv[cp_idx + 1] = cp_str + ":" + ":".join(new_jars)
    print(f"S3A JARs added: {new_jars}")
else:
    print("S3A JARs already in classpath (skipped)")

# JVM -D props (always set provider; only set endpoint/keys in MinIO mode —
# in cloud mode the JVM reads AWS_* from env via the Temporary provider).
jvm_props = [
    "-Dfs.s3a.impl=org.apache.hadoop.fs.s3a.S3AFileSystem",
    "-Dfs.s3a.path.style.access=true",
    f"-Dfs.s3a.aws.credentials.provider={provider}",
    "-Dspark.hadoop.fs.s3a.impl=org.apache.hadoop.fs.s3a.S3AFileSystem",
    "-Dspark.hadoop.fs.s3a.path.style.access=true",
    f"-Dspark.hadoop.fs.s3a.aws.credentials.provider={provider}",
    # Keep directory markers — don't delete parent-prefix markers (e.g. "users/")
    # outside the user's IAM scope on overwrite (harmless but noisy AccessDenied).
    "-Dfs.s3a.directory.marker.retention=keep",
    "-Dspark.hadoop.fs.s3a.directory.marker.retention=keep",
]
if mode == "minio":
    jvm_props += [
        f"-Dfs.s3a.endpoint={s3_endpoint}",
        f"-Dfs.s3a.access.key={access_key}",
        f"-Dfs.s3a.secret.key={secret_key}",
        f"-Dspark.hadoop.fs.s3a.endpoint={s3_endpoint}",
        f"-Dspark.hadoop.fs.s3a.access.key={access_key}",
        f"-Dspark.hadoop.fs.s3a.secret.key={secret_key}",
    ]

# Remove old -D props (idempotent), re-insert after java binary
argv = [a for a in argv if not (a.startswith("-Dfs.s3a.") or a.startswith("-Dspark.hadoop.fs.s3a."))]
for i, prop in enumerate(jvm_props):
    argv.insert(1 + i, prop)

k["argv"] = argv
k.setdefault("env", {})["HADOOP_CONF_DIR"] = conf_dir

with open(kernel_path, "w") as f:
    json.dump(k, f, indent=2)

print(f"kernel.json patched: {len(jvm_props)} JVM -D props (mode={mode})")
PYEOF
fi

# ── 4. Patch predef.sc for lazy val spark (Almond Scala kernel) ───────────────
PREDEF_FILE="/root/predef.sc"
if [ -f "$PREDEF_FILE" ] && [ -n "$AWS_ACCESS_KEY_ID" ]; then
  MODE="$MODE" PROVIDER="$PROVIDER" python3 - <<'PYEOF'
import re, os

predef_path = "/root/predef.sc"
mode = os.environ["MODE"]
provider = os.environ["PROVIDER"]
s3_endpoint = os.environ.get("S3_ENDPOINT", "")
access_key = os.environ.get("AWS_ACCESS_KEY_ID", "minioadmin")
secret_key = os.environ.get("AWS_SECRET_ACCESS_KEY", "minioadmin")

with open(predef_path) as f:
    content = f.read()

# Strip previously injected blocks (idempotent)
content = re.sub(r'// S3A config \(auto-injected by entrypoint\).*?(?=\nlazy val spark)', '', content, flags=re.DOTALL)
content = re.sub(r'\n  // S3A Hadoop conf \(auto-injected.*?println\("✅.*?\)\n', '\n', content, flags=re.DOTALL)

set_lines = [
    'SparkConfig.set("spark.hadoop.fs.s3a.impl", "org.apache.hadoop.fs.s3a.S3AFileSystem")',
    'SparkConfig.set("spark.hadoop.fs.s3a.path.style.access", "true")',
    f'SparkConfig.set("spark.hadoop.fs.s3a.aws.credentials.provider", "{provider}")',
]
hc_lines = [
    '_hc.set("fs.s3a.impl", "org.apache.hadoop.fs.s3a.S3AFileSystem")',
    '_hc.set("fs.s3a.path.style.access", "true")',
    f'_hc.set("fs.s3a.aws.credentials.provider", "{provider}")',
]
if mode == "minio":
    set_lines += [
        f'SparkConfig.set("spark.hadoop.fs.s3a.endpoint", "{s3_endpoint}")',
        f'SparkConfig.set("spark.hadoop.fs.s3a.access.key", "{access_key}")',
        f'SparkConfig.set("spark.hadoop.fs.s3a.secret.key", "{secret_key}")',
    ]
    hc_lines += [
        f'_hc.set("fs.s3a.endpoint", "{s3_endpoint}")',
        f'_hc.set("fs.s3a.access.key", "{access_key}")',
        f'_hc.set("fs.s3a.secret.key", "{secret_key}")',
    ]

s3a_block = "// S3A config (auto-injected by entrypoint)\n" + "\n".join(set_lines) + "\n"
hadoop_inject = "\n  // S3A Hadoop conf (auto-injected — applied after session creation)\n  val _hc = session.sparkContext.hadoopConfiguration\n  " + "\n  ".join(hc_lines) + f'\n  println("✅ S3A Hadoop conf applied: mode={mode}")\n'

content = content.replace("lazy val spark", s3a_block + "lazy val spark", 1)
content = content.replace("val session = builder.getOrCreate()", "val session = builder.getOrCreate()" + hadoop_inject, 1)

with open(predef_path, "w") as f:
    f.write(content)
print(f"predef.sc patched (mode={mode})")
PYEOF
fi

# ── 4b. trino() helper for Python notebooks (IPython startup) ─────────────────
# Removes the JDBC boilerplate: trino("SELECT …") / trino("catalog.schema.table").
# Uses the SSO token injected by the backend (OIDC_ACCESS_TOKEN) so the user
# queries Trino as themselves — no password in the notebook. The driver itself
# is NOT bundled here (orgs pick their own version): add the "Trino" connector
# preset (io.trino:trino-jdbc:<ver>) when connecting and this helper uses it.
IPY_STARTUP=/root/.ipython/profile_default/startup
mkdir -p "$IPY_STARTUP"
cat > "$IPY_STARTUP/00-sparklabx-trino.py" <<'PYEOF'
import os as _os


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
              .option("driver", "io.trino.jdbc.TrinoDriver"))
    token = _os.environ.get("OIDC_ACCESS_TOKEN")
    if token:
        reader = reader.option("accessToken", token)
    q = query.strip()
    if q.lower().startswith("select"):
        reader = reader.option("query", q)
    else:
        reader = reader.option("dbtable", q)
    return reader.load()
PYEOF
echo "trino() helper installed for Python kernels"

# ── 5. Start Jupyter Kernel Gateway ───────────────────────────────────────────
jupyter kernelgateway \
  --KernelGatewayApp.ip=0.0.0.0 \
  --KernelGatewayApp.port=8888 \
  --KernelGatewayApp.allow_origin='*' \
  --KernelGatewayApp.allow_headers='*' \
  --KernelGatewayApp.allow_methods='*' \
  --KernelGatewayApp.auth_token='' \
  --JupyterWebsocketPersonality.list_kernels=True
