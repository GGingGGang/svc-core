# E2E Runbook — 5M ("붙여넣기 → 추출 → 확정 → 이벤트 → 리마인더 sent/skipped")

> `../PLAN.md` §10 core 5M DoD: "전체 시나리오 통과 기록을 상태 보드에". 이 문서는 core 세션이 **주도**하는
> 전 구간 검증의 실행 절차 — 3개 서비스(auth/core/batch)가 실클러스터에서 실제로 연결돼 있어야 하고, 로컬
> sandbox(Docker 없음, 클러스터 접근 없음)에서는 실행할 수 없어 **사람이 이 문서를 따라 직접 실행**해야
> 한다. 통과하면 이 파일 맨 아래 "실행 기록"에 결과를 남기고 `../PLAN.md` §11 상태 보드 core 행을 갱신할 것.

## 0. 사전 확인

이 시나리오는 3개 서비스가 전부 최신 코드로 실배포돼 있고 서로 실제로 붙어야 의미가 있다. 아래가 안 되어
있으면 먼저 해결:

- [ ] `kubectl -n auth get pods` / `-n core` / `-n batch` — 전부 Running, RESTARTS 급증 없음
- [ ] core NS `gemini-api-key` Secret 존재 (`kubectl -n core get secret gemini-api-key`) — 없으면 4M 의
      `POST /schedules/extract` 가 502 (`HELPME_devops.md` #1)
- [ ] `../HELPME_devops.md` open 항목(#2 AuthorizationPolicy, #3 DB 부트스트랩 재확인) 상태 — 열려있어도
      이 시나리오 자체는 막히지 않지만(§9 Gemini egress 는 통제 범위 밖, DB 연결은 이미 살아있는 파드로 방증),
      기록은 해둘 것

## 1. 포트 포워딩 (외부 노출 상태를 가정하지 않음 — 항상 되는 방법)

```bash
kubectl -n auth port-forward svc/auth 3000:3000 &
kubectl -n core port-forward svc/core 8080:8080 &
```

## 2. auth — 계정 준비 + 토큰 발급

```bash
curl -s -X POST http://localhost:3000/register -H 'Content-Type: application/json' -d '{
  "email": "e2e-5m@example.com", "password": "correct horse battery staple",
  "display_name": "E2E", "timezone": "Asia/Seoul"
}'
# 이미 있으면 409 — 정상, 다음 단계로.

LOGIN=$(curl -s -X POST http://localhost:3000/login -H 'Content-Type: application/json' -d '{
  "email": "e2e-5m@example.com", "password": "correct horse battery staple"
}')
ACCESS_TOKEN=$(echo "$LOGIN" | jq -r .access_token)
echo "$ACCESS_TOKEN"   # 비어있으면 계정 잠김/실패 — 원인 확인 후 재시도
```

## 3. core — 텍스트 붙여넣기 → 추출 (4M)

```bash
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EXTRACT=$(curl -s -X POST http://localhost:8080/schedules/extract \
  -H "Authorization: Bearer $ACCESS_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"text\": \"내일 오후 3시 팀 회의\", \"now\": \"$NOW\", \"timezone\": \"Asia/Seoul\"}")
echo "$EXTRACT" | jq .
```

기대: `candidates` 배열에 1개 이상, `title`/`start_at` 채워짐. 502 가 나오면 §0 의 Gemini 키 확인부터.
429 가 나오면(Gemini rate limit) 잠시 후 재시도.

## 4. core — 후보 확정 (`POST /schedules`, `source=ai`)

```bash
CANDIDATE=$(echo "$EXTRACT" | jq '.candidates[0]')
SCHEDULE=$(curl -s -X POST http://localhost:8080/schedules \
  -H "Authorization: Bearer $ACCESS_TOKEN" -H 'Content-Type: application/json' \
  -d "$(echo "$CANDIDATE" | jq '{title, start_at, end_at, all_day, location, description, source: "ai", reminders: [{minutes_before: 0, channel: "push"}]}')")
echo "$SCHEDULE" | jq .
SCHEDULE_ID=$(echo "$SCHEDULE" | jq -r .id)
```

`minutes_before: 0` — 리마인더 스캔 잡이 이번 실행 주기 안에서 바로 `sent` 로 판정하게 하려는 의도적 선택
(생성 시각 = `start_at`, grace window 30분 이내이므로 `sent`). `skipped` 경로도 보고 싶으면 별도로
`start_at` 을 이미 지난 과거 시각(30분 이상 전)으로 만들어 두 번째 일정을 하나 더 만들 것.

## 5. core — 이벤트 발행 확인

```bash
curl -s http://localhost:8080/metrics | grep domain_event_published_total
```

`subject="app.schedules.created.v1"` 카운터가 이전 대비 최소 1 증가했는지 확인.

## 6. batch — reminder_dispatch 적재 + 스캔 결과 확인

batch 는 HTTP API 가 없으므로 DB 직접 조회. `app_batch` 계정 자격증명은 batch NS `db-creds` Secret:

```bash
kubectl -n batch get secret db-creds -o jsonpath='{.data.password}' | base64 -d; echo
kubectl -n batch run mysql-client --rm -it --image=mysql:8 --restart=Never -- \
  mysql -h <DB_HOST> -u app_batch -p batch --ssl-mode=REQUIRED
```

```sql
SELECT id, schedule_id, status, remind_at, sent_at
FROM reminder_dispatch
WHERE schedule_id = UNHEX(REPLACE('<SCHEDULE_ID>', '-', ''));
```

기대: 4단계에서 만든 스케줄의 리마인더 행이 존재하고, `status` 가 `pending` → (스캔 잡 주기, 기본 60초
내) `sent` 로 전이됨. 과거 시각으로 만든 두 번째 케이스는 `skipped`.

```sql
SELECT * FROM daily_schedule_stats WHERE stat_date = UTC_DATE();
```

`reminders_sent`/`reminders_skipped` 가 이 시나리오만큼 증가했는지 확인 (다른 트래픽이 없는 클러스터라면
정확한 수치 비교 가능).

## 7. 정리

```sql
DELETE FROM reminder_dispatch WHERE schedule_id = UNHEX(REPLACE('<SCHEDULE_ID>', '-', ''));
```

```bash
curl -s -X DELETE http://localhost:8080/schedules/$SCHEDULE_ID -H "Authorization: Bearer $ACCESS_TOKEN"
# kill port-forward background jobs
kill %1 %2 2>/dev/null
```

## 실행 기록

| 날짜 | 실행자 | 결과 | 비고 |
|------|--------|------|------|
| _(미실행)_ | | | 이 세션은 Docker/클러스터 접근이 없어 직접 실행하지 못함 — 위 절차만 작성 |
