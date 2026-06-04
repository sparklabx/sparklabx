#!/bin/sh
set -e

# Replace environment variables in the React build
# This allows runtime configuration of the frontend
echo "Configuring frontend with environment variables..."

# Set default only if variable is unset (not if it's empty)
# Empty string means use relative paths (nginx proxy)
if [ -z "${REACT_APP_API_URL+x}" ]; then
  REACT_APP_API_URL="http://localhost:3001"
fi

echo "Setting API URL to: ${REACT_APP_API_URL}"

# Overwrite env-config.js with correct value
cat <<EOF > /usr/share/nginx/html/env-config.js
window._env_ = {
  REACT_APP_API_URL: "${REACT_APP_API_URL}",
  VITE_GOOGLE_CLIENT_ID: "${VITE_GOOGLE_CLIENT_ID}"
};
EOF

# Also create the old config.js for backward compatibility
cat <<EOF > /usr/share/nginx/html/config.js
window.RUNTIME_CONFIG = {
  API_URL: "${REACT_APP_API_URL}",
  WS_URL: "${WS_URL:-ws://localhost:8080}",
  SPARK_API: "${SPARK_API:-/api/spark}",
  FLINK_API: "${FLINK_API:-/api/flink}",
  AIRFLOW_API: "${AIRFLOW_API:-/api/airflow}",
  KAFKA_API: "${KAFKA_API:-/api/kafka}",
  ENVIRONMENT: "${ENVIRONMENT:-development}"
};
EOF

echo "Frontend configuration complete"

# Execute the main command (nginx)
exec "$@"