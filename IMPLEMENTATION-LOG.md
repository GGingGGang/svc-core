# svc-core 구현 기록

## 진행 중: 일정·리마인더 API 계약

- `VIW-003~012`: `GET /schedules`는 `from`/`to`가 필수이고 최대 366일 범위만 조회한다. 기본 `limit=20`, 허용 범위 1~100이며 `(start_at,id)` keyset cursor를 쓴다. cursor는 사용자·기간·상태·검색어·표시 timezone에 묶인다. `q`는 제목/장소 literal substring 검색이며 `%`와 `_`를 와일드카드로 처리하지 않는다. 응답에 `schedules`와 nullable `next_cursor`가 항상 있다. 시간 일정은 기간 중첩, 종일 일정은 표시 timezone 기준 현지 날짜 범위 중첩으로 계산한다. 레거시 비한정 목록은 폐지했다.
- `SCH-010`: PATCH는 현재 revision을 포함한 강한 ETag의 `If-Match`를 요구한다. 새 요청의 오래된 revision은 412, 헤더 누락은 428이다. 동일 idempotency key와 동일 요청 본문/If-Match의 replay는 revision 비교보다 먼저 반환한다. 서로 다른 필드 PATCH는 행잠금 안에서 최신 행을 병합한다.
- `SCH-015`: bulk-delete는 모든 ID가 요청 사용자에게 속할 때만 원자적으로 삭제한다. foreign/missing ID가 하나라도 있으면 전체를 404로 거절한다.
- `SCH-017`: 복제 기능은 아직 구현하지 않았다. 안정된 요청·응답 계약과 회귀 검증이 남아 있다.
- 날짜/시간: IANA timezone과 date-only 종일 일정의 `[start_date,end_date)`를 저장한다. 기존 timezone 없는 일정은 UTC로 간주하고 기존 종일 일정 날짜도 UTC instant에서 보정한다. 사용자 timezone 변경으로 날짜를 다시 계산하지 않는다. local datetime의 DST gap은 422, fold는 `earlier|later`를 요구한다. UTC RFC3339 입력도 지원한다.
- `REM-002/003`: 일정당 리마인더는 최대 5개, offset은 0~10080분이며 중복 offset/channel은 409다. 신규 저장 채널은 Telegram만 허용하고 Notify의 내부 capability API에서 사용자 소유의 verified+consented 연결을 확인한다. 빈 목록은 리마인더 없음이다. 기존 push/email/none 행은 마이그레이션에서 제거하지 않는다.
- 내부 dispatch 검증: `/internal/reminders/{scheduleID}/validity`는 상수시간 Bearer 비교, 현재 일정/소유자/상태/리마인더, Auth 계정 활성 상태를 확인한다. revision이 달라져도 현재 channel+offset과 계산한 `remind_at`이 요청값과 같으면 유효하며 최신 일정 snapshot을 돌려준다. 시간/채널/상태 변경, 삭제, 비활성 계정은 `valid:false`다. 종일 일정의 dispatch 시각은 일정 timezone의 시작일 09:00에서 offset을 뺀 instant다.

## AI 원문 보존

- `AIX-016`: 추출 후보의 제목과 시각 구간이 겹치는 본인 일정이 있으면 `possible_duplicate`를 추가하고 `needs_confirmation=true`로 반환한다. 취소된 일정은 제외하며 어떤 기존 일정도 자동 삭제/교체하지 않는다.
- `AIX-017`: 새 extraction의 원문과 결과 JSON은 DB에 저장하지 않고 메타데이터만 남긴다. 기존 데이터를 migration에서 자동 삭제하지 않는다. `cmd/cleanup-extractions`는 필수 cutoff를 받아 기본 dry-run으로 대상 수를 보여주고, 명시적인 `--apply`에서만 cutoff 이전 원문/결과를 지운다. 기존 데이터 정리 실행은 운영 작업으로 남겨 두었다.
- `KEY-002`: BYOK 사용의 인증/요금제 우회 금지 및 공급자 오류 동작은 기존 AI 경로를 유지한다. 이번 변경은 extraction 원문 저장 여부에 한정한다.

## 남은 요구사항

- `SCH-017`: 일정 복제 API/검증/테스트.
- `EXT-001~009`: 반복 규칙·예외·ICS export와 import-preview/명시적 확정 저장은 미구현이다. 외부 임의 URL fetch를 허용하지 않는 preview ID/소유권/만료/중복 처리 계약과 bounded 반복 정책을 확정하고 구현해야 한다.
- `AIX-016`: 문서에 정의된 중복 감지 동작 및 중복 확인 회귀 테스트가 남아 있다.
- `POL-29`/계정 삭제: migration 000010에서 user UUID tombstone을 만든다. Auth-owned `ACCOUNT_LIFECYCLE`에 durable consumer를 연결해 삭제 요청을 처리하고, schedules/reminders, extraction metadata, create/mutation idempotency, AI admission state, 사용자 outbox payload를 같은 트랜잭션에서 제거한다. 삭제 처리 뒤 모든 쓰기 트랜잭션이 tombstone을 확인하고 410으로 거절한다. 결과 이벤트는 정리 완료 후 `completed`; 실패 시 `retry`를 보내 Auth 재처리를 요청한다. Preview/반복 테이블은 해당 기능 구현 때 같은 cleanup 트랜잭션에 포함해야 한다.

## 검증

- `go test ./...` 통과.
- `go test -tags=integration -run '^$' ./internal/api ./internal/db` 통과(통합 테스트 컴파일 확인).
- OpenAPI YAML 파싱 통과.
- Docker CLI/daemon을 사용할 수 없어 MySQL/NATS 컨테이너 통합 테스트는 실행하지 못했다.
- DST 경계, 리마인더 instant, 내부 토큰 비교의 단위 테스트를 추가했다. DB 의존 validity 시나리오(메타데이터 revision 유지 및 변경 instant 거부)와 계정 삭제 통합 시나리오도 작성했으나 Docker 부재로 실행은 확인하지 못했다.

## 최근 구현

- `SCH-017`: `POST /schedules/{id}/clone`으로 소유자 범위 안에서 일정을 복제한다. 제목/설명/장소/시간/시간대/all-day 날짜를 선택적으로 덮어쓸 수 있고, 미지정 필드는 원본 값을 유지한다. 새 UUID·revision 1로 생성하며 상태는 `confirmed`, 출처는 `manual`로 초기화한다. 원본 알림 정의는 Notify capability에서 현재 검증된 Telegram 채널일 때만 복제하고, 키 재사용 본문 충돌은 409로 거절한다. 기존 추출·dispatch 이력은 복사하지 않는다. 통합 테스트는 새 일정 생성, 알림 ID 재발급, 동일 키 재시도 및 교차 사용자 404를 확인한다.
- `AIX-016` API 응답 계약: 추출 후보 중복 의심 시 `needs_confirmation=true`, `issues`에 `possible_duplicate`를 포함한다. `duplicate_of` ID는 제공하지 않는다. 판정은 같은 사용자의 저장 일정 중 제목 trim/lower 일치 및 시간 구간 겹침이며 기존 일정을 수정하거나 삭제하지 않는다.
- 검증: `go test ./...` 통과, `git diff --check` 통과. Docker 통합 테스트 실행 환경은 사용할 수 없어 실제 DB/NATS 통합 실행은 미검증이다.
