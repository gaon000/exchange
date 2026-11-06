# 문서 지도

문서는 각자 하나의 책임을 갖는다. 작업 관련 문서를 선택해서 읽고, 같은 규칙을 여러 곳에서 따로 관리하지 않는다.

| 문서 | 소유하는 내용 |
| --- | --- |
| [README](../README.md) | 프로젝트 소개와 시작 문서 연결 |
| [AGENTS](../AGENTS.md) | AI와 사람을 위한 작업 진입점 |
| [Architecture](../ARCHITECTURE.md) | 현재 모듈 책임, 의존 방향, 시스템 경계 |
| [Development](development.md) | 환경 준비, 코드 규칙, 실제 실행 명령 |
| [Workflow](workflow.md) | 작업 시작, 계획 필요 여부, 검증과 완료 기준 |
| [ExecPlans](exec-plans/index.md) | 작업별 진행 상황, 결정, 검증 결과 |
| [Product Specs](product-specs/index.md) | 제품 목표, 사용자 동작, 정책과 미결 질문 |
| [Design Docs](design-docs/index.md) | 상태·저장·동시성·복구의 합의된 설계 |

## 작업별 읽기

[스냅샷 실패 구간과 정상 처리량](benchmarks/snapshot-window-capacity.md)은 마지막 응답까지 감싼 저장/GC/호스트 관측과 현재 버전의 정상 처리량 경계 탐색을 기록한다.

[낮은 요청률과 정수 JSON 비교](benchmarks/lower-rate-integer-comparison.md)는 정상 10,000/s 3/3 채택·snapshot 1,000/s 2/3 미채택과 JSON 할당 감소 후보의 정상 3회 비교를 기록한다. 전체 p99/CPU 차이는 관찰값이며 인과 효과·최대 처리량 향상으로 해석하지 않는다. [중단 시점 기록](benchmarks/lower-rate-progress.md)과 실패도 보존한다.

[현재 기준 재측정 결과](benchmarks/controlled-baseline.md)는 유휴 gate 후 정상 29,000/s·snapshot 3,000/s 각 3회를 검증했다. 0/3·2/3 통과로 두 조건 모두 미채택했으며 실패·워밍업 누락·창별 지연을 보존한다.

[현재 기준 반복 측정](controlled-baseline.md)은 clean 생산 바이너리·유휴 gate·전원·예산·고정 조건과 실패 보존을 설명한다.

[실험 결과 비교 보고서](experiment-comparison.md)는 기준·후보 원본 재검증, 조건 차이·무효/실패 보존과 JSON/Markdown 생성 명령을 설명한다.

[재현 가능한 실험 도구](diagnostic-tools.md)는 로컬 var 전용 도구를 저장소 CLI로 정리한 실행·검증·분석 안내다. 짧은 smoke와 명시적 10분 preset, 전체 복구, 과거 묶음 재집계, trace/OS I/O·타이머 대조를 제공한다.

실제 거래와 복구를 빠르게 확인하려면 [한 명령 거래 데모](trading-demo.md)를 실행한다. 주문·부분 체결·잔량 취소·잔고·정상 재시작의 기대값과 보존 파일을 안내한다.

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

v1의 범위와 거래 규칙은 [제품 명세](product-specs/exchange.md), 주문 상태·잔고·로그·중복 계약은 [상세 설계](design-docs/exchange-v1.md), 구체적인 입력·저장 형식은 [API](design-docs/api-v1.md)와 [Storage](design-docs/storage-v1.md)를 읽는다. [단계별 구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)은 P4까지 완료했고 P5 HTTP 계층·초기화/실행 CLI·HTTP 장애 통합 검증을 연결했다. P6의 [HTTP 부하 생성·집계·원본 기록](design-docs/benchmark-v1.md)을 구현했다. 서버 구간·프로세스 자원 계측을 추가했다. 스냅샷/프로파일 옵션과 새 프로세스 실행·환경/바이너리/원본 묶음을 연결했다. 반복 기준 측정을 완료했고 P7 정렬 이동 후보의 비교를 마쳤다. 후보는 미채택했으며 [스냅샷 처리 한계](benchmarks/p7-snapshot-capacity.md)는 추가 5회 탐색으로 3,000 requests/s를 세 번 채택했다. 실제 전원 장애 검증은 미완료다. [P1 벤치마크](benchmarks/p1-memory-engine.md)는 엔진 단독 측정이다. [P6 기준과 결과](benchmarks/p6-baseline.md)는 전체 경로 초기 탐색·고정 조건·14개 실행의 재검증과 정상 조건의 반복 baseline을 기록한다. [P7 정렬 이동 비교](benchmarks/p7-snapshot-sorting.md)는 25회 실행과 미채택 판정·기준 구현 복원을 기록한다. 작은 수정은 관련 기준과 실제 경로를 확인한다. 완료 계획은 과거 판단을 이해하는 자료이며 현재 구현과 다시 대조한다.

[실제 OS·전원 장애 시험 절차](power-loss-validation.md)는 별도 시험 장비·외부 관찰자·재시도 전 조회·미확정 명령의 판정과 보존 자료를 정한다. 현재는 예제의 정상 재시작만 검증했으며 실제 전원 장애 시험은 미완료다.

## 선택 문서

Product Specs는 사용자 동작·범위를, Design Docs는 주문 상태·잔고·영구 저장·복구의 구체적인 계약을 소유한다. 이번에 합의된 설계를 문서화하면서 Design Docs를 추가했다.

작업의 결정 이유는 설계의 대안 비교와 실행 계획의 Decision Log에 남긴다. 별도 Decisions·Glossary·skill은 아직 도입하지 않았다. 필요한 경우에만 추가하고 실제 존재하는 문서를 지도에 연결한다.

## 템플릿 적용 이력

| 원본 저장소·조회 위치 | 기준 ref | 확인한 commit·버전 | 적용일 | 템플릿 경로 → 대상 경로 | 변경 요약 |
| --- | --- | --- | --- | --- | --- |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-28 (Asia/Seoul) | `starter/AGENTS.md` → `AGENTS.md`; `starter/ARCHITECTURE.md` → `ARCHITECTURE.md`; `starter/docs/` → `docs/`; `optional/product-specs/` → `docs/product-specs/` | 최초 도입. 빈 저장소의 현재 상태, 거래소 구축 목표, 기존 사용자 개발 규칙을 반영. `README.md`와 `docs/product-specs/exchange.md`는 프로젝트 전용 문서로 추가. 템플릿의 패키지 설정·검사기·CI는 복사하지 않음. |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-29 (Asia/Seoul) | `optional/design-docs/index.md`, `optional/design-docs/template.md` → `docs/design-docs/index.md`, `docs/design-docs/template.md` | 선택 설계 영역 추가. index를 프로젝트에 맞게 조정. `exchange-v1.md`와 active 구현 계획은 합의한 내용을 별도 작성. 전체 템플릿 재적용은 아님. |

기존 사용자 수정과 이전 적용 이력을 보존하고 새 적용 내역을 추가한다. 재적용 시 이전 기준 버전과 현재 문서를 비교하되, 기준 버전이 없거나 접근할 수 없으면 그 한계를 기록한다. 이 기록은 문서 적용의 근거이며 애플리케이션 실행·테스트·배포 완료의 증거가 아니다.
