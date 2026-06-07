# ---- Stage 1: Build React App with Vite ----
FROM node:22-alpine AS builder

WORKDIR /app
COPY package*.json ./

RUN npm ci --legacy-peer-deps
COPY . .

ENV NODE_ENV=production
ARG VITE_GOOGLE_CLIENT_ID
ARG VITE_MICROSOFT_CLIENT_ID
ARG VITE_MICROSOFT_TENANT_ID
RUN npm run build

# ---- Stage 2: Serve with Nginx (root, port 80) ----
FROM mirror.gcr.io/library/nginx:alpine

COPY nginx-prod.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/build /usr/share/nginx/html

EXPOSE 80
# Use 127.0.0.1 instead of `localhost` — Alpine's wget tries IPv6 first
# and nginx only binds IPv4 (no `listen [::]:80;` in the conf), so
# `wget http://localhost/` returns "connection refused" and marks the
# container unhealthy even though it serves traffic fine on IPv4.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -qO- http://127.0.0.1/ || exit 1

CMD ["nginx", "-g", "daemon off;"]
