# 문서 지도

문서는 각자 하나의 책임을 갖는다. 작업 관련 문서를 선택해서 읽고, 같은 규칙을 여러 곳에서 따로 관리하지 않는다.

| 문서 | 소유하는 내용 |
| --- | --- |
| [README](../README.md) | 프로젝트 소개와 시작 문서 연결 |
| [AGENTS](../AGENTS.md) | AI와 사람을 위한 작업 진입점 |
| [Architecture](../ARCHITECTURE.md) | 현재 모듈 책임, 의존 방향, 시스템 경계 |
| [프로젝트 결과 보고서](project-results.md) | 기준 코드·현재 결과물·검증된 복구 범위·시점별 성능 근거와 미완료 한계 |
| [Development](development.md) | 환경 준비, 코드 규칙, 실제 실행 명령 |
| [Workflow](workflow.md) | 작업 시작, 계획 필요 여부, 검증과 완료 기준 |
| [ExecPlans](exec-plans/index.md) | 작업별 진행 상황, 결정, 검증 결과 |
| [Product Specs](product-specs/index.md) | 제품 목표, 사용자 동작, 정책과 미결 질문 |
| [Design Docs](design-docs/index.md) | 상태·저장·동시성·복구의 합의된 설계 |

## 작업별 읽기

전체 결과부터 읽으려면 [프로젝트 결과 보고서](project-results.md)를 따른다. 기준 코드는 2026-11-12 master 3a23e1b이며, 과거 측정의 producer와 구분한다. 실제 OS·전원 장애 검증은 미완료·보류다.

[정상 세 조건 재측정](benchmarks/normal-capacity-recheck.md)은 신규 2,000/3,000/s 각 3/3·5,000/s 1/3 통과와 이전 실패를 포함한 누적 24개 판정을 구분한다.

[스냅샷 실패 구간과 정상 처리량](benchmarks/snapshot-window-capacity.md)은 저장/GC/호스트 대조와 재개 후 정상 관찰 채택점 2,000/s·인접 3,000/s 미채택을 기록한다. 이전 실패/중단 포함 15개 판정과 병행 브라우저 부하를 보존하며 서버의 내재 최대 용량으로 확정하지 않는다.

[낮은 요청률과 정수 JSON 비교](benchmarks/lower-rate-integer-comparison.md)는 정상 10,000/s 3/3 채택·snapshot 1,000/s 2/3 미채택과 JSON 할당 감소 후보의 정상 3회 비교를 기록한다. 전체 p99/CPU 차이는 관찰값이며 인과 효과·최대 처리량 향상으로 해석하지 않는다. [중단 시점 기록](benchmarks/lower-rate-progress.md)과 실패도 보존한다.

[2026-11-06 과거 채택점 재측정](benchmarks/controlled-baseline.md)은 유휴 gate 후 정상 29,000/s·snapshot 3,000/s 각 3회를 검증했다. 0/3·2/3 통과로 두 조건 모두 미채택했으며 실패·워밍업 누락·창별 지연을 보존한다.

[현재 기준 반복 측정](controlled-baseline.md)은 clean 생산 바이너리·유휴 gate·전원·예산·고정 조건과 실패 보존을 설명한다.

[실험 결과 비교 보고서](experiment-comparison.md)는 기준·후보 원본 재검증, 조건 차이·무효/실패 보존과 JSON/Markdown 생성 명령을 설명한다.

[재현 가능한 실험 도구](diagnostic-tools.md)는 로컬 var 전용 도구를 저장소 CLI로 정리한 실행·검증·분석 안내다. 짧은 smoke와 명시적 10분 preset, 전체 복구, 과거 묶음 재집계, trace/OS I/O·타이머 대조를 제공한다.

[상태 크기별 실제 결과](benchmarks/state-size-costs.md)는 6조건 각 5회의 메모리/snapshot/복구 비용·완전한 bytes 일치와 한계를 기록한다. [측정 도구](state-size-costs.md)는 활성 주문형/체결·취소 이력형의 메모리·snapshot 단계·두 복구 경로를 동일 fixture/새 프로세스에서 반복 측정한다.

[스냅샷 CPU·할당 프로파일링](snapshot-profiling.md)은 같은 fixture의 공개 생성/복구 경로를 별도 새 프로세스에서 계측한다. [실제 분석](benchmarks/snapshot-profiling.md)은 190 worker의 함수별 CPU·할당 근거와 후속 후보를 정리한다.

[WAL 전용 디코더 최적화](benchmarks/wal-decoder-optimization.md)는 동일 1만 명령 두 상태·두 복구 경로의 전후 각 5회에서 전체 bytes·복구 계약을 유지한 시간/할당 변화를 비교한다. [동결·재집계 명령](wal-decoder-comparison.md)을 함께 따른다.

[스냅샷 항목 직렬화 최적화](benchmarks/snapshot-leaf-serialization.md)는 표준 Encoder 재사용으로 같은 상태의 생성 임시 할당을 줄이고 전후 각 5회의 bytes/저장/복구 일치·느린 표본을 확인한다. [동결·비교 명령](snapshot-leaf-comparison.md)을 함께 따른다.

[스냅샷 이후 명령 복구·비용](snapshot-tail-recovery.md)은 같은 1만 명령 상태의 이후 명령 0·1천·5천개를 각 5회 측정하고 후속 체결·취소·최초 응답을 확인한다. [실제 결과](benchmarks/snapshot-tail-recovery.md)를 함께 따른다.

[최신 스냅샷 읽기 프로파일링](snapshot-read-profiling.md)은 이후 명령 복구 실험의 같은 두 최종 상태에서 최신 코드의 CPU/할당을 별도 수집하고 읽기·디코딩·복원·재확인 경로를 분리한다. [분석과 다음 후보](benchmarks/snapshot-read-profiling.md)를 함께 따른다.

[디코더 읽기 버퍼 대조](decoder-read-buffer.md)는 디코더만 32/64/128/256KiB로 바꾼 네 빌드에서 같은 두 상태·세 tail을 각 5회 비교하고 별도 ReaderAt/Pread 진입 계수를 확인한다. [실제 결과](benchmarks/decoder-read-buffer.md)를 함께 따른다.

[고정 seed 거래·복구 검사](random-recovery.md)는 주문·취소·중복 ID·snapshot·정상 재시작을 조합하고 최초 응답/전체 상태를 대조하며 실패 명세를 한 명령으로 재현한다.

[실험 자료 패키징·복원](evidence-packages.md)은 이전 실행·고정 빌드·비교 기준을 함께 묶고, 새 폴더의 복원 자료만으로 검증·분석·JSON/Markdown 비교표를 재생성한다.

[실험 자료 보관·정리 미리보기](artifact-retention.md)는 var의 용량·참조 그래프·원본/고정 빌드 보호와 삭제 후보의 JSON/Markdown 미리보기를 제공한다.

실제 거래와 복구를 빠르게 확인하려면 [한 명령 거래 데모](trading-demo.md)를 실행한다. 주문·부분 체결·예약금/자기 거래·ID 충돌과 WAL/snapshot/SIGKILL 복구의 기대값·선택 모드·보존 파일을 안내한다.

[거래용 CLI](trading-cli.md)는 사용자가 지정한 계정·가격·수량으로 주문/취소하고 상태·최초 결과를 조회하며, 전송 전에 저장한 요청으로 같은 ID를 재시도하는 명령을 안내한다.

[스냅샷 유무 10분 비교](benchmarks/soak-10m-snapshot-comparison.md)는 100 requests/s에서 각 10분 실행·전체 상태 복구·자원 증가를 보고한다. 복구는 두 조건 모두 확인했고, 요청 누락·구간별 지연 기준 미달은 별도로 기록했다.

[스냅샷 단계 측정](benchmarks/snapshot-stage-breakdown.md)은 같은 상태의 5회 반복에서 생성·복구 비용을 분리한다. 복구 decode와 반복 직렬화가 큰 비용이며 전체 상태 byte 일치와 로컬 회귀 검증을 확인했다.

[복구 디코더 개선 비교](benchmarks/snapshot-decoder-optimization.md)는 기존 검증 계약을 유지한 후보의 동일 상태 5쌍 결과를 기록한다. 전체 복구 중앙값 32.5%, decode 56.4% 감소와 상태 bytes 일치를 확인했다.

[생성 중복 직렬화 개선 비교](benchmarks/snapshot-preflight-optimization.md)는 같은 상태에서 새 파일 생성 전후 각 5회·별도 재시작을 검증한다. 생성 중앙값 29.7%, 크기 검사 79.4% 감소와 느린 표본 1개를 함께 기록했다.

[최적화 버전 10분 재측정](benchmarks/optimized-soak-10m-comparison.md)은 같은 부하 설정에서 끔/켬 각 600초와 전체 복구를 이전 결과와 비교한다. 복구와 켬의 CPU·평균 생성 비용은 개선됐으나 전체 p99는 높았고 켬의 생성기 미전송 9건을 그대로 기록했다.

[지연 구간 분석과 생성기 진단](benchmarks/latency-diagnostics.md)은 기존 10분 표본의 서버 저장 지연과 생성기 지연을 대조하고, 다음 실험을 위한 선택적 전달 경계·CPU/GC 계측을 설명한다. 원인은 확정하지 않았으며 새 장시간 반복 측정은 별도 단계다.

[진단 계측 10분 반복](benchmarks/diagnostic-soak-repeats.md)은 끔/켬 각 3회와 전체 복구 일치, scheduler/worker 미전송 8/7건을 기록한다. 켬 세 실행 모두 구간별 지연 초과가 남았고 원인은 미확정이다.

[실행 trace·호스트 진단](benchmarks/trace-host-diagnosis.md)은 끔/켬 각 600초에서 timer/select·runnable 대기를 구분하고 긴 WAL sync의 Syscall 상태를 확인했다. 50ms 미전송은 재현하지 못했으며 두 실행의 전체 복구는 일치했다.

[타이머 분리·저장 OS I/O 관찰](benchmarks/timer-os-io-diagnosis.md)은 타이머만·worker 전달만 각 10분에서 지연 경계를 확인하고, 별도 거래 10분의 실제 C 저장 호출과 프로세스 I/O 누계를 대조했다. 관리자 커널 추적은 미확보이며 전체 복구는 일치했다.

[타이머 trace 켬·끔 대조](benchmarks/timer-trace-control.md)는 같은 binary의 네 조건 각 10분에서 trace 비활성에서도 50ms 한도 초과가 발생함을 확인했다. 켬/끔의 인과 효과는 호스트 상태 차이로 미확정이며 monotonic·UTC 차이도 기록했다.

v1의 범위와 거래 규칙은 [제품 명세](product-specs/exchange.md), 주문 상태·잔고·로그·중복 계약은 [상세 설계](design-docs/exchange-v1.md), 입력과 저장 형식은 [API](design-docs/api-v1.md)와 [Storage](design-docs/storage-v1.md)를 따른다. [측정 계약](design-docs/benchmark-v1.md)과 [P6 고정 조건](benchmarks/p6-baseline.md)은 판정 기준을 소유한다. [P1 엔진 단독 측정](benchmarks/p1-memory-engine.md), 과거 P6/P7와 이후 캠페인의 수치를 전체 경로의 현재 성능으로 혼합하지 않는다. 현재 결과는 [종합 보고서](project-results.md), 단계별 증거와 필수 미완료는 [v1 구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)에서 확인한다. 완료 계획은 당시 판단의 이력이며 현재 코드와 다시 대조한다.

[실제 OS·전원 장애 시험 절차](power-loss-validation.md)는 별도 시험 장비·외부 관찰자·재시도 전 조회·미확정 명령의 판정과 보존 자료를 정한다. 소프트웨어의 정상 재시작·snapshot·SIGKILL 후 복구는 검증했지만 실제 OS·전원 장애 시험은 별도 장비가 없어 미완료·보류다.

## 선택 문서

Product Specs는 사용자 동작·범위를, Design Docs는 주문 상태·잔고·영구 저장·복구의 구체적인 계약을 소유한다. 이번에 합의된 설계를 문서화하면서 Design Docs를 추가했다.

작업의 결정 이유는 설계의 대안 비교와 실행 계획의 Decision Log에 남긴다. 별도 Decisions·Glossary·skill은 아직 도입하지 않았다. 필요한 경우에만 추가하고 실제 존재하는 문서를 지도에 연결한다.

## 템플릿 적용 이력

| 원본 저장소·조회 위치 | 기준 ref | 확인한 commit·버전 | 적용일 | 템플릿 경로 → 대상 경로 | 변경 요약 |
| --- | --- | --- | --- | --- | --- |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-28 (Asia/Seoul) | `starter/AGENTS.md` → `AGENTS.md`; `starter/ARCHITECTURE.md` → `ARCHITECTURE.md`; `starter/docs/` → `docs/`; `optional/product-specs/` → `docs/product-specs/` | 최초 도입. 빈 저장소의 현재 상태, 거래소 구축 목표, 기존 사용자 개발 규칙을 반영. `README.md`와 `docs/product-specs/exchange.md`는 프로젝트 전용 문서로 추가. 템플릿의 패키지 설정·검사기·CI는 복사하지 않음. |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-29 (Asia/Seoul) | `optional/design-docs/index.md`, `optional/design-docs/template.md` → `docs/design-docs/index.md`, `docs/design-docs/template.md` | 선택 설계 영역 추가. index를 프로젝트에 맞게 조정. `exchange-v1.md`와 active 구현 계획은 합의한 내용을 별도 작성. 전체 템플릿 재적용은 아님. |

기존 사용자 수정과 이전 적용 이력을 보존하고 새 적용 내역을 추가한다. 재적용 시 이전 기준 버전과 현재 문서를 비교하되, 기준 버전이 없거나 접근할 수 없으면 그 한계를 기록한다. 이 기록은 문서 적용의 근거이며 애플리케이션 실행·테스트·배포 완료의 증거가 아니다.
