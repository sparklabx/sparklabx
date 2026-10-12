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

# Sign-up: Google email only, no password -> pending, can never password-login.
req POST /auth/register "" '{"email":"not-an-email"}'; check "register rejects bad email" 400
req POST /auth/register "" '{"email":"'$U'@ex.com","note":"demo"}'; check "register (email only)" 201
req POST /auth/register "" '{"email":"'$U'@EX.com"}'; check "register duplicate (case-insensitive)" 409
req GET /admin/users "$ADMIN"; check "admin lists users" 200
RID=$(echo "$BODY" | python3 -c "import json,sys; print([u['id'] for u in json.load(sys.stdin) if u['email']=='$U@ex.com'][0])")
ROW=$(echo "$BODY" | python3 -c "import json,sys; u=[u for u in json.load(sys.stdin) if u['email']=='$U@ex.com'][0]; print(u['status'], u['signup_note'])")
[ "$ROW" = "pending demo" ] && echo "PASS listed as pending with note" || { echo "FAIL row=$ROW"; fail=1; }
req PUT /admin/users/$RID/access "$ADMIN" '{}'; check "approve registration (default 4h)" 200
req POST /admin/login "" '{"username":"'$U'","password":"Password123"}'; check "registered user has no password login" 401
# Reject: request removed (can re-apply), applicant mailed.
req POST /auth/register "" '{"email":"r'$U'@ex.com"}'; check "register second applicant" 201
req GET /admin/users "$ADMIN"
RJ=$(echo "$BODY" | python3 -c "import json,sys; print([u['id'] for u in json.load(sys.stdin) if u['email']=='r$U@ex.com'][0])")
req POST /admin/users/$RJ/reject "$ADMIN"; check "reject pending request" 200
req POST /admin/users/$RJ/reject "$ADMIN"; check "reject is pending-only / idempotent" 404
req POST /admin/users/$RID/reject "$ADMIN"; check "cannot reject an approved user" 404
req POST /auth/register "" '{"email":"r'$U'@ex.com"}'; check "rejected email can apply again" 201
# Mail (only when a Mailpit API is given, e.g. MAILPIT=http://localhost:18025)
if [ -n "${MAILPIT:-}" ]; then
  sleep 2
  mails=$(curl -s "$MAILPIT/api/v1/messages" | python3 -c "import json,sys; print('\n'.join(m['To'][0]['Address']+'|'+m['Subject'] for m in json.load(sys.stdin)['messages']))")
  echo "$mails" | grep -q "^$U@ex.com|Your SparkLabX trial is approved" && echo "PASS approval mail sent" || { echo "FAIL approval mail: $mails"; fail=1; }
  echo "$mails" | grep -q "^r$U@ex.com|Your SparkLabX trial request" && echo "PASS rejection mail sent" || { echo "FAIL rejection mail: $mails"; fail=1; }
fi

# Time-boxed session: a password user created by the superadmin (Google login can't run headless).
req POST /admin/users "$ADMIN" '{"username":"t'$U'","email":"t'$U'@ex.com","password":"Password123"}'; check "superadmin creates user" 201
ID=$(echo "$BODY" | j "['id']")
req PUT /admin/users/$ID/access "$ADMIN" '{"hours":-1}'; check "negative hours rejected" 400
req PUT /admin/users/$ID/access "$ADMIN" '{"hours":0.003}'; check "time-box to ~11s" 200
req POST /admin/login "" '{"username":"t'$U'","password":"Password123"}'; check "approved user logs in" 200
T=$(echo "$BODY" | j "['token']")
req GET /admin/me "$T"; check "me" 200; [ -n "$(echo "$BODY" | j "['access_expires_at']")" ] && echo "PASS me has access_expires_at" || { echo "FAIL no access_expires_at"; fail=1; }
req GET /notebooks "$T"; check "trial user lists notebooks" 200
for p in /admin/users /allowed-domains; do req GET $p "$T"; check "trial user blocked from $p" 403; done
req GET /minio/buckets "$T"; check "trial user lists own storage scopes" 200
req GET /notebooks/storage/files "$T"; check "trial user lists notebook files" 200
req GET /connectors "$T"; check "trial user lists data sources" 200
req PUT /admin/users/$ID/access "$T" '{"hours":100}'; check "trial user cannot extend itself" 403
req POST /auth/refresh "$T"; check "refresh" 200
T2=$(echo "$BODY" | j "['token']"); req GET /notebooks "$T2"; check "refreshed token works" 200

sleep 12
req GET /notebooks "$T2"; check "expired trial is cut off mid-session" 401 access_expired
req POST /auth/refresh "$T2"; check "expired trial cannot refresh" 401 access_expired
req POST /admin/login "" '{"username":"t'$U'","password":"wrong-pass"}'; check "wrong password stays generic" 401
req POST /admin/login "" '{"username":"t'$U'","password":"Password123"}'; check "expired trial cannot log in" 403 access_expired

req PUT /admin/users/$ID/access "$ADMIN" '{"hours":0}'; check "set no limit" 200
req POST /admin/login "" '{"username":"t'$U'","password":"Password123"}'; check "unlimited user logs in again" 200
T3=$(echo "$BODY" | j "['token']")
req POST /admin/users/$ID/revoke "$T3"; check "user cannot revoke anyone" 403
ADMIN_ID=$(curl -s "$B/admin/me" -H "Authorization: Bearer $ADMIN" | j "['id']")
req POST /admin/users/$ADMIN_ID/revoke "$ADMIN"; check "superadmin cannot be revoked" 404
req POST /admin/users/$ID/revoke "$ADMIN"; check "revoke" 200
req GET /notebooks "$T3"; check "revoked user is cut off immediately" 401 access_expired
req PUT /admin/users/$ID/access "$ADMIN" '{"hours":1}'; check "re-grant after revoke" 200
req POST /admin/login "" '{"username":"t'$U'","password":"Password123"}'; check "re-granted user logs in" 200
req GET /notebooks "bogus.jwt.token"; check "bad token gets session_expired code" 401 session_expired
req DELETE /admin/users/$ID "$ADMIN"; check "delete user" 200
req GET /notebooks "$T2"; check "deleted user's session is cut off" 401 account_removed
exit $fail
