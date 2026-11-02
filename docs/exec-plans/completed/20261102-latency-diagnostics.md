# 지연 구간 분석과 생성기 진단 계측

상태: Completed
종료일: 2026-11-02

[ExecPlan 운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

최근 10분 부하의 생성기 미전송·응답 지연을 기존 원본으로 분석하고, 다음 실험에서 생성기와 서버의 지연을 구분할 수 있도록 부족한 계측을 추가한다. 이번 범위는 분석·계측 구현·검증이다. 조건별 10분 3회 반복, 엔진 최적화, 실제 전원 장애, 커밋·병합·push는 포함하지 않는다.

## Progress

- [x] 2026-11-02: clean master 6876da7에서 feature branch 생성, 기존 측정·생성기·서버 계측 계약 확인.
- [x] 2026-11-02: 기존 입력 9개 해시 확인, 두 조건의 5초 구간 240개와 서버 누적 histogram 차분 분석. 원인 미확정과 UTC/monotonic 정렬 한계 기록.
- [x] 2026-11-02: 선택적 생성기 전달 경계·주기 자원 계측, 취소/한도/부분 파일 보존, experiment 원본 검증 구현.
- [x] 2026-11-02: benchreport의 진단 원본 재집계·series 분리·기존 기준 미채택 연결. 관련 race·최종 portable 200개/native 232개 테스트와 문서 검증 통과.

## Surprises & Discoveries

기존 GENERATOR_LATE는 scheduler와 worker 양쪽에서 기록하지만 어느 경계였는지는 남지 않는다. 생성기 CPU/GC는 Run 전후 값만 있다. 서버는 500ms 누적 histogram·자원 표본이 있어 구간 차분이 가능하나 요청별 상관관계와 정확한 단계 시작 시각은 없다. 절대 시각과 monotonic offset의 차이도 확인하고 해석에 반영한다.

스냅샷 끔 500~515초에는 전체 관측의 WAL sync 20ms 초과 27건 중 24건이 집중됐다. 생성기 전송 지연은 작았다. 켬의 9건 미전송은 두 구간으로, 첫 구간은 전송 지연도 크고 관찰 당시 snapshot 호출이 진행 중이지 않았다. 두 번째는 snapshot capture와 겹치나 인과관계는 증명되지 않는다. UTC 경과와 monotonic 경과의 종료 시 차이는 끔 -9.724ms, 켬 +1.483ms다.

기존 benchreport는 완료 파일 6개만 기대했다. 새 진단 실행은 8개를 검증하도록 연결하되 원래 무진단 기준으로 채택되지 않도록 별도 사유를 남긴다. 기존 기준과 결과를 바꾸지 않는다.

## Decision Log

- 2026-11-02: 기존 원본은 변경하지 않고 별도 분석 산출물에 결과를 기록한다. 관측 중첩을 인과 증명으로 쓰지 않는다.
- 2026-11-02: 진단은 기본 비활성으로 제공하고 기존 요청 계획·누락 판정·시간 제한·저장/복구 계약을 유지한다. 진단 자체 비용과 샘플링 한계를 문서화한다.
- 2026-11-02: loadgen/experiment 공통 `--diagnostics-interval` 옵션으로 dispatch와 process 표본을 함께 켠다. 주기는 100ms~1m, 0은 비활성이다. workload 파일 자체는 바꾸지 않는다.
- 2026-11-02: 수집 최대 40,000 periodic+initial/final, 입력별 dispatch는 기존 최대 200만 건으로 제한한다. 한도 도달은 부분 원본 보존 후 실행 실패로 기록한다. 요청 성능 summary와 진단 완료 실패를 구분한다.

## Outcomes & Retrospective

기존 지연 구간을 서버 저장·생성기 실행/전송 경계로 좁혔지만 근본 원인은 미확정이다. 다음 실험에서 scheduler→worker와 생성기 CPU/GC를 대조할 수 있는 선택적 진단을 구현했다. 실제 요청 계획·측정 유효성·거래 API/저장 계약은 유지했다. 구현·로컬 검증을 완료했다. 장시간 반복 측정·계측 비용 측정은 별도 단계다. feature branch에 변경을 남겼으며 커밋·병합·push 및 새 원격 CI 실행은 하지 않았다.

## Context and Orientation

[최신 보고서](../../benchmarks/optimized-soak-10m-comparison.md)의 현재 끔/켬 원본은 `var/soak-optimized-20261102.yawwm75d`다. [생성기](../../../internal/bench/runner.go)는 무대기열 전달과 worker 재검사로 늦은 요청을 전송하지 않는다. [loadgen](../../../cmd/loadgen/command.go)은 Run 이후 표본을 저장한다. [측정 계약](../../design-docs/benchmark-v1.md)은 누락·실패를 포함한 분모와 원본 완료 표식을 정의한다.

## Plan of Work

원본의 지연 표본과 서버 단계/GC 차분을 대조한다. 생성기의 전달 전·worker 실행 관찰을 구분하고 CPU/GC를 주기적으로 기록하는 선택 옵션을 추가한다. 수집은 메모리에 제한된 양만 보존하고 측정 후 파일에 저장한다. 실험 실행기에서 진단 원본의 존재·해시·설정 일치도 검증한다.

## Validation and Acceptance

실제 예정 입력·기존 표본 형식과 기본 동작을 보존한다. scheduler/worker 지연, 포화, 취소·drain, 수집 지연·한도, 출력 실패와 완료 원본 검증을 검사한다. 관련 Go 테스트, 전체 portable/native CI 스크립트와 문서 검증을 수행한다. 짧은 통합 시험은 성능 개선이나 장시간 검증으로 보고하지 않는다.

- `scripts/go test -race ./internal/bench ./cmd/loadgen ./cmd/experiment -timeout=120s` 통과. 기본 무진단·scheduler 지연·worker 관찰 모순 거절·포화·취소와 collector 합류/누락/상한, 실제 HTTP/APFS 진단 원본을 검사했다.
- `scripts/go test -race ./tools/benchreport -timeout=60s` 통과. 진단 주기별 series 분리·기존 요청 통계 재현·기존 기준 미채택을 검사했다.
- 최종 portable `var/ci/portable.51X8t9`: 200개/18개 패키지, 포맷·vet·build 포함 통과.
- 최종 native `var/ci/native.F847VI`: 전체 race 232개/18개 패키지, 명시적 child helper skip 1개. 포맷·vet·build·APFS probe 포함 통과.
- 첫 테스트의 loopback bind는 sandbox에서 거절됐으며 승인된 실행 환경에서 실제 HTTP 테스트를 다시 수행해 통과했다. 이 환경 오류를 애플리케이션 테스트 통과로 계산하지 않았다.
- Python 분석의 입력 9개, 비교용 기존 Go 분석 2개의 해시 확인. 기존 5초 집계 240개와 새 요청 집계 및 서버 histogram 전체/구간 합계 일치. 문서 42개/파일 238개 링크·경로·자리표시자 진단 0개 확인.

## Idempotence and Recovery

기존 측정 폴더·run을 재사용하지 않는다. 새 파일은 exclusive create로 만들고 오류 시 부분 원본을 보존한다. 프로세스 시험은 소유한 자식만 제어한다. 거래·WAL·snapshot 형식은 변경하지 않는다.

## Artifacts and Notes

- [분석 보고서](../../benchmarks/latency-diagnostics.md), [선택 구간 JSON](../../benchmarks/20261102-latency-diagnostics.json), [진단 계약](../../design-docs/benchmark-v1.md#생성기-진단-옵션).
- `var/latency-diagnostics-20261102/analyze.py`, `analysis.json`, `verify_report.py`, `artifact-manifest.json` 보존. 기존 원본은 변경하지 않았다. 새 10분 실행 0회.
- 과거 완료 계획·성능 기준은 당시 기록으로 유지한다. 별도 장비의 실제 전원 장애는 보류다.

## Interfaces and Dependencies

부하 진단 옵션과 선택적 산출물만 추가한다. production dependency 추가 없음. 표준 Go/Python 도구를 사용한다.
