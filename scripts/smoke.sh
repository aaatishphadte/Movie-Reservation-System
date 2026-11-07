#!/usr/bin/env sh
# End-to-end smoke test against a running API (default http://localhost:8080).
# Requires curl and jq.
set -eu
API="${API:-http://localhost:8080}/api/v1"
EMAIL="user$(date +%s)@test.local"

step() { printf '\n== %s\n' "$*"; }

step "admin login"
ADMIN=$(curl -sf "$API/auth/login" -d '{"email":"admin@cinema.local","password":"Admin@12345"}' | jq -r .access_token)

step "signup + login regular user"
curl -sf "$API/auth/signup" -d "{\"email\":\"$EMAIL\",\"name\":\"Smoke\",\"password\":\"password123\"}" | jq -c .
USER=$(curl -sf "$API/auth/login" -d "{\"email\":\"$EMAIL\",\"password\":\"password123\"}" | jq -r .access_token)

step "regular user is forbidden from admin routes"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $USER" "$API/admin/reports/summary"

step "admin creates a movie and schedules a showtime"
MOVIE=$(curl -sf -H "Authorization: Bearer $ADMIN" "$API/admin/movies" \
  -d '{"title":"Smoke Test Movie","description":"e2e","duration_minutes":100,"genres":["Comedy"]}' | jq -r .id)
START=$(date -u -d '+3 days' +%Y-%m-%dT10:00:00Z 2>/dev/null || date -u -v+3d +%Y-%m-%dT10:00:00Z)
ST=$(curl -sf -H "Authorization: Bearer $ADMIN" "$API/admin/showtimes" \
  -d "{\"movie_id\":$MOVIE,\"hall_id\":1,\"starts_at\":\"$START\",\"price_cents\":1500}" | jq -r .id)
echo "showtime $ST"

step "overlapping showtime in same hall is rejected (409)"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $ADMIN" "$API/admin/showtimes" \
  -d "{\"movie_id\":$MOVIE,\"hall_id\":1,\"starts_at\":\"$START\",\"price_cents\":1500}"

step "schedule for the day"
curl -sf "$API/showtimes?date=${START%%T*}" | jq -c '.movies[] | {title, n: (.showtimes|length)}'

step "seat map, hold two seats, reserve with idempotency key"
SEATS=$(curl -sf "$API/showtimes/$ST/seats" | jq -c '[.seats[0].id, .seats[1].id]')
curl -sf -H "Authorization: Bearer $USER" "$API/showtimes/$ST/holds" -d "{\"seat_ids\":$SEATS}" | jq -c .
RES=$(curl -sf -H "Authorization: Bearer $USER" -H "Idempotency-Key: smoke-1" "$API/reservations" \
  -d "{\"showtime_id\":$ST,\"seat_ids\":$SEATS}")
echo "$RES" | jq -c '{id, status, total_cents, seats: (.seats | map(.row + (.number|tostring)))}'
RID=$(echo "$RES" | jq -r .id)

step "retry with same key replays (200, same id)"
curl -s -D - -o /dev/null -H "Authorization: Bearer $USER" -H "Idempotency-Key: smoke-1" "$API/reservations" \
  -d "{\"showtime_id\":$ST,\"seat_ids\":$SEATS}" | grep -i -e '^HTTP' -e 'Idempotent'

step "second booking of the same seats conflicts (409)"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $USER" "$API/reservations" \
  -d "{\"showtime_id\":$ST,\"seat_ids\":$SEATS}"

step "my upcoming reservations"
curl -sf -H "Authorization: Bearer $USER" "$API/reservations?scope=upcoming" | jq -c '[.items[] | {id, status, total_cents}]'

step "admin reports"
curl -sf -H "Authorization: Bearer $ADMIN" "$API/admin/reports/summary" | jq -c .totals
curl -sf -H "Authorization: Bearer $ADMIN" "$API/admin/reports/showtimes" | jq -c "[.items[] | select(.showtime_id==$ST)]"

step "cancel reservation, seats become available again"
curl -sf -X POST -H "Authorization: Bearer $USER" "$API/reservations/$RID/cancel" | jq -c '{id, status}'
curl -sf "$API/showtimes/$ST/seats" | jq -c '{available, capacity}'

step "admin cancels showtime, then deletes movie"
curl -sf -X POST -H "Authorization: Bearer $ADMIN" "$API/admin/showtimes/$ST/cancel" | jq -c .
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE -H "Authorization: Bearer $ADMIN" "$API/admin/movies/$MOVIE"

printf '\nSMOKE OK\n'
