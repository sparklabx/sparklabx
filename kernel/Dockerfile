# SparkLabX notebook kernel image: Jupyter Kernel Gateway + Spark + S3A support.
# Used by all three KERNEL_MODE deployment options (shared, docker_per_user,
# k8s_per_user). The image installs hadoop-aws + AWS SDK so s3a:// reads from
# MinIO or any S3-compatible store work out of the box.
#
# Base image is built from github.com/sparklabx/notebook-kernel (Almond fork
# bundled with Jupyter Kernel Gateway + PySpark Connect). Multi-arch
# (linux/amd64 + linux/arm64) — runs natively on M-series Macs and Graviton.
FROM ghcr.io/sparklabx/kernel-base:latest

WORKDIR /home/exam

# Add only hadoop-aws + AWS SDK bundle JARs for s3a:// protocol
# Download directly — do NOT use coursier fetch (it copies 255 transitive deps causing classpath conflicts)
ENV PYSPARK_JARS=/usr/local/lib/python3.11/dist-packages/pyspark/jars
RUN wget -q -P $PYSPARK_JARS \
    https://repo1.maven.org/maven2/org/apache/hadoop/hadoop-aws/3.3.4/hadoop-aws-3.3.4.jar \
    https://repo1.maven.org/maven2/com/amazonaws/aws-java-sdk-bundle/1.12.262/aws-java-sdk-bundle-1.12.262.jar

# Pre-warm the dependency caches for the DEFAULT Trino connector so the first
# trino() connect is instant instead of doing a slow Ivy/coursier round-trip to
# Maven. This warms the resolver caches only — Ivy (/root/.ivy2) for PySpark's
# spark.jars.packages, coursier (/root/.cache/coursier) for Almond's $ivy — it
# does NOT bake the jar onto the classpath, so users keep full freedom to choose
# any trino-jdbc version (only the default is pre-cached; others resolve on
# demand). Keep TRINO_JDBC_VERSION in sync with the frontend "Trino" preset.
ENV TRINO_JDBC_VERSION=481
RUN python3 -c "from pyspark.sql import SparkSession; SparkSession.builder.master('local[1]').config('spark.jars.packages','io.trino:trino-jdbc:${TRINO_JDBC_VERSION}').getOrCreate().stop()" \
 && coursier fetch "io.trino:trino-jdbc:${TRINO_JDBC_VERSION}" > /dev/null \
 && echo "pre-warmed Trino JDBC ${TRINO_JDBC_VERSION} (ivy + coursier)"

# DataFlint (Spark performance UI) is ON BY DEFAULT for PySpark AND Scala —
# pre-warm both resolver caches: Ivy (/root/.ivy2) for PySpark's
# spark.jars.packages, coursier (/root/.cache/coursier) for Almond's $ivy — so
# the default-injected dependency resolves offline. Does NOT bake it onto the
# classpath. Keep DATAFLINT_VERSION in sync with the frontend default in NotebookPage.tsx.
ENV DATAFLINT_VERSION=0.9.9
RUN python3 -c "from pyspark.sql import SparkSession; SparkSession.builder.master('local[1]').config('spark.jars.packages','io.dataflint:spark_2.12:${DATAFLINT_VERSION}').config('spark.plugins','io.dataflint.spark.SparkDataflintPlugin').getOrCreate().stop()" \
 && coursier fetch "io.dataflint:spark_2.12:${DATAFLINT_VERSION}" > /dev/null \
 && echo "pre-warmed DataFlint ${DATAFLINT_VERSION} (ivy + coursier)"

# Spark S3 config (STS credentials injected via env vars at pod creation)
ENV SPARK_CONF_DIR=/home/exam/spark-conf
RUN mkdir -p $SPARK_CONF_DIR
COPY spark-defaults.conf $SPARK_CONF_DIR/spark-defaults.conf

# Notebook helpers (trino() for Python + Scala) — committed source, installed by
# entrypoint.sh into the IPython startup dir / Almond predef at container start.
COPY helpers /home/exam/helpers

# Startup script
COPY entrypoint.sh /home/exam/entrypoint.sh
RUN chmod +x /home/exam/entrypoint.sh

EXPOSE 8888

ENTRYPOINT ["/home/exam/entrypoint.sh"]
