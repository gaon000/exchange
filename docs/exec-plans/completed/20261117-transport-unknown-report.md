# 전송 오류·UNKNOWN 분석 보고서 자동화

상태: Completed
시작일: 2026-11-17
종료일: 2026-11-17

## Purpose / Acceptance

master efe1864의 선택적 전송 단계/유형과 고정 장애 재현을 읽기 전용 분석에 연결한다. 명세에 선택한 봉인 loadgen/장애 시나리오 자료를 검증하고 단계·유형·phase별 건수, 요청 run/계정/종류/ID·지연과 UNKNOWN을 JSON/Markdown으로 생성한다. 측정 당시 판정·UNKNOWN과 이후 결과 조회를 구분하고 404 UNKNOWN·202 PENDING은 미확정으로 유지한다. 과거 진단/지연 부재는 null/미계측이며 0이나 원인 추정으로 바꾸지 않는다.

기존 10분 끔/켬 자료·봉인된404 후속 조회·기존/신규 실제 장애 재현 자료로 검증한다. 서버 호출·POST/자동 재시도·보관 바이너리 실행·새 장시간 부하·원본 변경은 하지 않는다. 현재 소스로 만든 Go checker만 실행해 loadgen 원본을 재집계한다. 신규 장애 재현에는 executor의 monotonic 경과만 추가하고 기존 거래/저장/복구 계약은 유지한다. 새 dependency·커밋/병합/푸시는 범위 밖이다.

## Progress

- [x] 현재 기준/계약·기존 분석·자료 형식 확인, 기능 브랜치 생성.
- [x] 제한된 명세·봉인/경로/identity 검증·시점 분리·JSON/Markdown 생성.
- [x] 새 장애 자료 지연 기록·회귀/변조/과거 호환 테스트.
- [x] 기존10분/404·기존/신규 장애 자료의 실제 보고서·재생성·원본 불변 확인.
- [x] 전체 portable/native CI·문서/결과 기록.

## Surprises & Discoveries

기존 loadgen에는 단계/유형이 없는 과거 UNKNOWN1건이 있다. 당시 후속404는 별도 봉인 묶음의 result.json과 서버 ready identity로 연결할 수 있다. 고정 장애 재현의 기존 transport 파일에는 경과 시간이 없으므로 새 선택 필드를 추가해야 한다. 같은 요청의 재시도 횟수와 논리 명령 수, 전체 phase와 본 측정은 구분한다.

기존 최신10분 두 child는 생성기 미전송으로 completed 대신 failed.json이 있다. 상위 suite 봉인은 완료됐으므로 실패 marker와 모든 raw hash를 상위 manifest/Go 재집계와 대조해 분석하고 loadgenComplete=false/measurementValid=false를 유지한다. 정상 성공 자료만 요구해 실패를 버리면 이번 진단 목적과 어긋난다.

## Decision Log

- 2026-11-17: experiment.py transport-report --spec --output을 추가한다. 명세와 모든 입력은 hash/봉인을 검증하고 새 출력에만 기록한다. 원문 오류를 새 보고서에 복제하지 않는다.
- 2026-11-17: 후속 조회 자료는 명세로 지정한 봉인 파일/같은 run·계정·kind·ID에만 연결한다. 200의 명시적 최초 결과와 202/404 관측을 분리한다. 추후 조회로 원래 실패 표본/성능 판정을 수정하지 않는다.
- 2026-11-17: 새 장애 시나리오의 지연은 호출 직전~반환 monotonic 경과다. 과거 자료의 지연과 소급 비교하지 않는다. 전원 장애는 미완료·보류이고 v1 Blocked 유지.

## Outcomes & Retrospective

구현과 실제 자료 검증을 완료했다. 최종 출력은 `var/transport-report-final-20261117`, 별도 재생성은 `var/transport-report-reproduced-20261117`이며 report.json/report.md/manifest.json/completed.json이 모두 bytes 일치했다. 최종 JSON·Markdown은 docs/benchmarks/20261117-transport-unknown-report.json 및 20261117-transport-unknown-tables.md로 복사했고, 입력은 20261117-transport-unknown-spec.json에 고정했다. 입력 source/봉인을 검증하고 원본을 수정하지 않았다.

기존10분 끔은 오류0/본 측정 UNKNOWN0, 켬은 오류1/UNKNOWN1이며 양쪽 measurementValid=false/loadgenComplete=false를 유지했다. 켬의 bob/place/soak-v1-o-38457(MEASURE38457)는4,662,542ns·단계/유형 null이다. 별도 봉인 receipt의404 UNKNOWN은 UNRESOLVED로 남았고 미접수로 재분류하지 않았다. 기존 장애3건은 지연 null, 새 `var/transport-report-fixture-20261117`의3건은 실제 monotonic 지연이 있다. 양쪽 headers/eof2·read/unexpected_eof1, 최초UNKNOWN3과 후속200의 RESULT_OBSERVED3을 구분하고 중간404도 보존했다. 새 시나리오 자체는 고유명령5/체결2/정상 재시작3·WAL-only3회 PASSED다. 이 지연3개는 처리량/성능 개선 근거가 아니다.

검증:

- `scripts/go test -race ./tools/recoverycheck -count=1 -timeout=120s`: 실제 저장/HTTP/복구 통과.
- Python 보고서 경계8개 및 `python3 -O`에서도8개 통과. Loadgen checker adapter 단위 테스트는 경계용 stub이며 아래 실제 자료/통합 smoke와 구분한다.
- `bash scripts/ci.sh portable`: `var/ci/portable.GOD6Ey`,245개 Go/111개 Python·format/vet/build·진단 smoke, exit0.
- `bash scripts/ci.sh native`: `var/ci/native.V2G4C6`,285개 Go race(명시적 자식 helper skip1)/111개 Python·진단 smoke, exit0. 새 부하/실제 장애 자료의 report CLI 생성·봉인 검증·no-reuse·reseal한 raw 의미 변조 거절도 통과.
- 독립 기대값으로 오류0/1/3/3·ID/4,662,542ns·원래 UNKNOWN/무효/404 미확정·과거null/새지연>0을 확인했다.
- 문서 상대 경로, 모든 새/수정 파일 공백, gofmt·git diff --check 통과.

초기 실측 분석은 loadgen completed만 요구해 실패했으며 출력 폴더를 만들지 않았다. 상위 봉인/raw 검증을 근거로 failed child를 허용한 뒤 최종 실측/회귀를 통과했다. 첫 `var/transport-report-20261117`은 중간 코드 결과로 보존하며 최신/동결 보고서와 구분한다.

실제 전원 장애는 미완료·보류이며 v1 Blocked를 유지한다. 구현 완료 당시에는 커밋·merge·push/GitHub CI를 수행하지 않았다.

## 2026-11-17 검토·발행

사용자가 검토·커밋·master 병합/푸시와 실제 GitHub CI 확인을 승인했다. 기준/원격 master는 efe1864로 일치했다.

검토에서 loadgen 요청 정보의 대조를 보완했다. 기존 Go checker는 manifest 설정에서 계획을 재생성해 표본을 집계하지만 plan.jsonl의 각 행은 파일 hash만 검사했다. 새 보고서는 해당 파일의 ID/phase를 읽으므로, 재해시된 다른 계획 행이 요청 정보를 잘못 연결할 수 있었다. opt-in --verify-plan으로 preparation/warmup/measurement의 전체 행을 strict JSON/순서·개수·요청/본문/시각까지 재생성 계획과 대조하고 report는 planVerified=true를 요구한다. 기존 두 인자 checker의 출력은 보존한다. Go 변조/행 누락·초과·순서/중복 키 회귀와 native CLI의 reseal한 phase 변조 거절을 추가했다.

보완 후 `scripts/go test ./tools/experimentcheck` 및 보고서 Python8개(일반/-O)가 통과했다. 전체 `bash scripts/ci.sh portable`은 `var/ci/portable.8xfbQy`에서247개 Go/111개 Python·smoke, `bash scripts/ci.sh native`는 `var/ci/native.wFbeWr`에서287개 race(명시적 자식 helper skip1)/111개 Python·smoke를 exit0으로 마쳤다. native smoke에서 다시 봉인한 phase 변조도 거절했다.

`var/transport-report-reviewed-20261117` 및 `var/transport-report-reviewed-replay-20261117`의 JSON/Markdown/manifest/completed가 모두 bytes 일치했다. 이전 동결 보고서 대비 counts/attempts/originalMeasurement/unknownCommands/transportGroups 및 해석이 모두 동일하며 새 planVerified 증거와 source hash만 갱신했다. 자동 JSON·Markdown을 최신 결과로 교체했고 이전 원본/보고서를 보존했다. 남은 알려진 결함은 없다. 커밋·병합·푸시 이후 실제 GitHub CI는 병합 SHA와 모든 job을 대조해 최종 답변에 보고한다. 전원 장애는 미완료·보류이고 v1 Blocked 유지.
