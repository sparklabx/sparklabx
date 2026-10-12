#!/usr/bin/env bash
# API checks for trial access: register -> pending -> approve (time-boxed) -> admin-only APIs -> expiry.
# Runs against a live stack with a superadmin "admin" / $ADMIN_PASSWORD, e.g. after `docker compose up`:
#   B=http://localhost:3000/api/v1 ADMIN_PASSWORD=... scripts/test-trial-access.sh
set -uo pipefail
B=${B:-http://localhost:3000/api/v1}   # through the frontend nginx, like a browser
fail=0
j() { python3 -c "import json,sys; d=json.load(sys.stdin); print(d$1 if d is not None else '')" 2>/dev/null; }
req() { # method path token [json] -> sets CODE, BODY
  local m=$1 p=$2 t=$3 d=${4:-}
  local out; out=$(curl -s -w '\n%{http_code}' -X "$m" "$B$p" -H 'Content-Type: application/json' ${t:+-H "Authorization: Bearer $t"} ${d:+-d "$d"})
  CODE=${out##*$'\n'}; BODY=${out%$'\n'*}
}
check() { # name expected_code [expected_json_code]
  local ok=1; [ "$CODE" = "$2" ] || ok=0
  if [ -n "${3:-}" ]; then [ "$(echo "$BODY" | j "['code']")" = "$3" ] || ok=0; fi
  if [ $ok = 1 ]; then echo "PASS $1"; else echo "FAIL $1: got $CODE $BODY"; fail=1; fi
}
U=alice$RANDOM
req POST /admin/login "" '{"username":"admin","password":"'"${ADMIN_PASSWORD:?set ADMIN_PASSWORD}"'"}'; check "admin login" 200
ADMIN=$(echo "$BODY" | j "['token']")
req GET /auth/config ""; check "auth config" 200; [ "$(echo "$BODY" | j "['signup_enabled']")" = "True" ] && echo "PASS signup_enabled" || { echo "FAIL signup_enabled"; fail=1; }

req POST /auth/register "" '{"username":"Bad Name","email":"x@y.z","password":"Password123"}'; check "register rejects bad username" 400
req POST /auth/register "" '{"username":"'$U'","email":"'$U'@ex.com","password":"short"}'; check "register rejects short password" 400
req POST /auth/register "" '{"username":"'$U'","email":"'$U'@ex.com","password":"Password123","note":"demo"}'; check "register" 201
req POST /auth/register "" '{"username":"'$U'","email":"'$U'@ex.com","password":"Password123"}'; check "register duplicate" 409
req POST /admin/login "" '{"username":"'$U'","password":"Password123"}'; check "pending user cannot log in" 403 pending_approval
req POST /admin/login "" '{"username":"'$U'","password":"wrong-pass"}'; check "wrong password stays generic" 401

req GET /admin/users "$ADMIN"; check "admin lists users" 200
ID=$(echo "$BODY" | python3 -c "import json,sys; print([u['id'] for u in json.load(sys.stdin) if u['username']=='$U'][0])")
STATUS=$(echo "$BODY" | python3 -c "import json,sys; print([u['status'] for u in json.load(sys.stdin) if u['username']=='$U'][0])")
[ "$STATUS" = pending ] && echo "PASS listed as pending" || { echo "FAIL status=$STATUS"; fail=1; }

req PUT /admin/users/$ID/access "$ADMIN" '{"hours":-1}'; check "negative hours rejected" 400
req PUT /admin/users/$ID/access "$ADMIN" '{"hours":0.003}'; check "approve for ~11s" 200
req POST /admin/login "" '{"username":"'$U'","password":"Password123"}'; check "approved user logs in" 200
T=$(echo "$BODY" | j "['token']")
req GET /admin/me "$T"; check "me" 200; [ -n "$(echo "$BODY" | j "['access_expires_at']")" ] && echo "PASS me has access_expires_at" || { echo "FAIL no access_expires_at"; fail=1; }
req GET /notebooks "$T"; check "trial user lists notebooks" 200
for p in /admin/users /allowed-domains /minio/buckets; do req GET $p "$T"; check "trial user blocked from $p" 403; done
req PUT /admin/users/$ID/access "$T" '{"hours":100}'; check "trial user cannot extend itself" 403
req POST /auth/refresh "$T"; check "refresh" 200
T2=$(echo "$BODY" | j "['token']"); req GET /notebooks "$T2"; check "refreshed token works" 200

sleep 12
req GET /notebooks "$T2"; check "expired trial is cut off mid-session" 401 access_expired
req POST /auth/refresh "$T2"; check "expired trial cannot refresh" 401 access_expired
req POST /admin/login "" '{"username":"'$U'","password":"Password123"}'; check "expired trial cannot log in" 403 access_expired

req PUT /admin/users/$ID/access "$ADMIN" '{"hours":0}'; check "set no limit" 200
req POST /admin/login "" '{"username":"'$U'","password":"Password123"}'; check "unlimited user logs in again" 200
req GET /notebooks "bogus.jwt.token"; check "bad token gets session_expired code" 401 session_expired
req DELETE /admin/users/$ID "$ADMIN"; check "delete user" 200
req GET /notebooks "$T2"; check "deleted user's session is cut off" 401 account_removed
exit $fail
