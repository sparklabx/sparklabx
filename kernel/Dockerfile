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

# Spark S3 config (STS credentials injected via env vars at pod creation)
ENV SPARK_CONF_DIR=/home/exam/spark-conf
RUN mkdir -p $SPARK_CONF_DIR
COPY spark-defaults.conf $SPARK_CONF_DIR/spark-defaults.conf

# Startup script
COPY entrypoint.sh /home/exam/entrypoint.sh
RUN chmod +x /home/exam/entrypoint.sh

EXPOSE 8888

ENTRYPOINT ["/home/exam/entrypoint.sh"]
