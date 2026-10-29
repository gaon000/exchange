# P6 전체 경로 측정 기준과 결과

상태: In Progress — 초기 탐색과 비교 기준 고정 완료, 반복 기준 측정 미완료.
기준 고정일: 2026-10-29 (Asia/Seoul).

## 범위와 근거

공개 HTTP 요청부터 APFS의 F_FULLFSYNC·순차 적용·전체 응답까지를 측정한다. [측정 계약](../design-docs/benchmark-v1.md)의 시간·분모·중복·오류 정의를 사용한다. 이 문서는 P6의 초기 실측, 고정 기준, 반복 baseline을 소유한다. P7에서 결과를 본 뒤 기준을 유리하게 바꾸지 않는다. 초기 탐색 수치와 아래 고정 조건의 baseline은 다르다.

## 초기 탐색

소스 `cb47f918216dea9d1f72cf6a68dc70a35b83d5b3`, modified=false인 바이너리를 사용했다. Go 1.27.3, Apple M3 Pro·18 GiB RAM, macOS 15.8 (24H23), 로컬 APFS·4,096-byte block이며 AC 전원 연결을 확인했다. 동일 개발 컴퓨터에서 서버와 생성기를 함께 실행했다. 다른 앱의 자원 사용·열 상태를 완전히 통제한 전용 벤치마크 장비는 아니다.

각 rate에서 새 run, seed=17, 거래 계정 3개와 무잔고 거절 계정, 양쪽 10 level, 주문 수량 1 lot, Rest/Marketable/Cancel/Duplicate/RejectProbe=35/30/10/15/10을 사용했다. 워밍업 2초·측정 10초, 생성기 동시 한도 2,048·발사 지연 한도 50ms·요청 deadline 5초, 서버 기본 배치/큐, 500ms 계측이었다. snapshot과 profiler는 껐다. 아래 p99는 측정 cohort이며 예정 p99는 고유 신규 승인 주문의 원래 예정 시각부터 성공 관찰까지다.

| 예정 요청/s | 고유 신규 승인/s | 요청 p99 (ms) | 예정 p99 (ms) | 생성기 누락 | 측정 유효 |
| --- | --- | --- | --- | --- | --- |
| 100 | 65.7 | 6.844 | 8.680 | 0 | 예 |
| 500 | 327.4 | 9.970 | 10.296 | 0 | 예 |
| 1,500 | 979.7 | 8.706 | 9.065 | 0 | 예 |
| 5,000 | 3,249.6 | 6.838 | 6.942 | 0 | 예 |
| 10,000 | 6,485.6 | 6.753 | 6.844 | 0 | 예 |
| 20,000 | 13,006.2 | 7.198 | 7.372 | 0 | 예 |
| 40,000 | 25,998.9 | 13.278 | 14.398 | 0 | 예 |
| 70,000 | 42,088.0 | 56.980 | 73.470 | 56,479 | 아니오 |

측정 구간에서 관찰한 timeout·과부하 거절은 모두 0이었다. 70,000/s에서는 GENERATOR_CAPACITY=56,438, GENERATOR_LATE=41로 예정 700,000개 중 643,521개만 실제 실행됐다. 성공한 일부 표본의 42,088 신규 주문/s를 정상 최대 지속 처리량으로 채택하지 않는다. 원본 표본·summary·실패 기록을 보존했다. 40,000/s의 주기 관찰 최대 command queue는 599개, 마지막 프로세스 peak RSS는 904,724,480bytes였다. 이는 관찰 최대/프로세스 생애 peak이며 순간 실제 최대 큐/측정 구간 전용 RSS가 아니다.

원본은 Git 제외 경로 `var/p6-initial-dgzi6jmf/`에 있다. 각 normal-RATE 디렉터리는 바이너리·설정·raw 표본·서버 계측을 포함한다. 유효한 일곱 실행에는 completed.json과 해시가 있고, 70,000/s에는 failed.json이 있다. parent의 overview.json은 편의 집계이며 원본의 대체물이 아니다. 최초 편의 집계에서 peakRssBytes 필드명의 대소문자 오류로 스크립트가 멈췄지만 100/s 실험은 이미 정상 완료됐다. 해당 실험을 다시 실행하지 않고 필드명 수정 후 남은 rate만 이어갔다.

## 고정 비교 조건 v1

기계 판독 기준은 [criteria-v1.json](../../config/benchmarks/criteria-v1.json), 초기 상태는 [config-v1.json](../../config/benchmarks/config-v1.json), workload 원형은 [workload-v1.json](../../config/benchmarks/workload-v1.json)이다. rate만 탐색 변수로 바꾸고 나머지 workload·초기 자금·판정은 유지한다. 파일의 40,000/s는 탐색 시작점이며 통과가 보장된 목표가 아니다. [benchreport](../../tools/benchreport/main.go)로 판정하며 반복 baseline 결과는 후속 작업이다.

| 조건 | 고정 값과 의미 |
| --- | --- |
| 지연 상한 | 전체 측정의 RequestLatency p99와 UniqueNewOrderScheduledLatency p99가 각각 20ms 이하 |
| 구간별 지연 | 예정 시각 기준 5초 창 4개 각각의 RequestLatency·ScheduledAttemptLatency p99가 20ms 이하. 빠른 마지막 구간이나 중복만으로 전체 p99를 낮추지 않음 |
| 실패 허용 | 워밍업/측정의 생성기 누락 0. 과부하·서버/전송 timeout·서버/전송 오류·잘못된 응답 0. 미해결 UNKNOWN 0. 예상한 업무 거절·취소 no-op은 성능 실패와 분리 |
| 기간 | 워밍업 5초, 측정 20초. 기간을 줄여 높은 rate를 통과시키지 않음 |
| 반복 | 동일 조건/소스/바이너리마다 독립된 새 run 최소 3회. 해당 조건의 모든 실행이 통과해야 채택하며 실패한 반복도 보고 |
| 시간 안정성 | 각 5초 창에 서버 계측 최소 8개. 마지막 창의 관찰 command queue 평균이 첫 창보다 64개 넘게 증가하면 탈락. 창별 p99와 함께 해석 |
| 탐색 해상도 | 예정 요청률 1,000/s. 큰 간격으로 범위를 찾은 뒤 가장 높은 채택 rate와 바로 위 rate를 확인. 최고 채택 rate의 고유 신규 주문/s를 반복 범위로 보고 |
| 부하/초기 상태 | seed 17, namespace baseline-v1, 거래 계정 alice/bob/carol·hotAccountPercent=0, 별도 reject 계정. 위 입력 비율·수량·가격·깊이와 파일의 초기 자금을 고정 |
| 데이터 범위 | fresh run의 준비 20개+25초 입력. rate에 따라 누적 이력 크기는 달라지며 보고서에 명령/주문/체결·WAL·메모리 크기를 기록. 같은 rate의 baseline/P7은 같은 입력 prefix와 초기 상태 |
| 생성기 | maxInFlight=2,048, requestTimeout=5s, maxLateness=50ms. 누락은 유효성 실패이며 지연 상한은 예정 시각 기준 지표로도 검사 |
| 서버 기준 | 기본 command/query 큐 8,192/1,024, HTTP/runtime 한도 16,384, pendingWaiters=16, 배치 256개/1 MiB/1ms. P7은 검증할 변수 하나만 바꾸고 실제 인자를 기록 |
| 계측 | 500ms 주기. 정상·snapshot·profile·snapshot-profile을 별도 조건으로 분리 |
| snapshot | 켠 조건에서 주기 5초, 측정과 겹친 완료 이벤트 최소 2개. 완료가 없는 설정만으로 snapshot 측정이라 하지 않음 |
| profile | CPU·종료 heap/allocs, 기본 MemProfileRate를 기록. 프로파일 수치를 정상 성능 결과에 합산하지 않음 |

20ms는 초기 탐색의 저·중부하 p99 약 7~10ms에 여유를 두면서, 70,000/s에서 나타난 수십 ms의 대기와 생성기 누락을 정상으로 인정하지 않도록 정한 실험 기준이다. 서비스 SLA나 사용자가 요구한 모든 요청의 최대 지연 보장이 아니다.

100만 예정 시도 상한으로는 5+20초에서 40,000/s까지만 측정할 수 있었다. 초기 탐색에서 그 이상의 범위를 확인할 필요가 있어 생성기의 유한 상한을 200만으로 늘렸다. rate 상한 100만/s·전체 시간 1시간, snapshot 2 GiB 파일 한도와 거래 규칙은 유지한다. 이 조건에서는 최대 80,000/s까지 같은 기간으로 요청할 수 있다. 초기 config의 maxCommands도 200만이며 실제 고유 명령 수는 중복을 제외해 그보다 작다. 메모리·파일 비용은 늘어나므로 그 영향을 숨기지 않는다. 아직 200만 시도 전체의 실행 성능을 검증했다는 의미는 아니다.

## 원본 재검증과 판정 도구

[benchreport](../../tools/benchreport/main.go)는 실행이 끝난 묶음을 오프라인으로 읽는다. 서버나 부하 생성기를 시작하지 않으며 측정과 동시에 실행하지 않는다. 최대 100개 묶음을 받아 서로 다른 조건을 같은 반복으로 합치지 않고 JSON 보고서를 만든다. 보고서는 기존 실험 디렉터리 밖의 새 파일만 허용한다. 디렉터리 alias도 해석해 원본 묶음 안에 보고서를 쓰거나 같은 경로를 중복 입력하는 것을 거절한다.

- completed.json의 모든 파일 해시와 파일 집합을 확인한다. 누락·미기재 파일·symlink/특수 파일·변조는 evidence 오류다. loadgen의 6개 해시·plan digest·run/config identity도 다시 대조한다.
- 설정·seed·runId로 Plan을 재구성하고 plan.jsonl의 모든 요청을 비교한다. 전체 samples.jsonl을 [Summarize](../../internal/bench/summary.go)로 다시 계산해 저장된 summary와 일치해야 한다. 새 집계가 틀릴 가능성은 독립 기대값의 단위 시험으로 별도 확인하며 재집계만으로 매칭 정확성까지 증명한다고 주장하지 않는다.
- 전체 지연/오류와 [구간 집계](../../internal/bench/windows.go)를 판정한다. 창은 예정 시각으로 나누며, 응답이 다음 창이나 drain까지 늦어져도 원래 창에 남긴다. 서버 계측의 startedUTC를 같은 창에 대응시키고 최소 관찰 수·큐 평균 증가를 검사한다.
- 고정 workload/config·계측 주기·snapshot/profile 설정·관찰 환경과 바이너리 revision을 검사한다. revision 미상/modified 빌드는 채택하지 않는다. snapshot 완료 이벤트와 실제 측정의 교집합을 다시 세고 profile 시각·압축 무결성을 확인한다.
- source/binary·config·workload(rate 제외)·runtime/HTTP 설정·시나리오·관찰 하드웨어/환경별 series를 만든 뒤 rate별 반복을 모은다. 모든 포함 실행이 통과하고 서로 다른 runId가 3개 이상이어야 qualified=true다. 복사한 run은 양쪽 모두 중복으로 탈락하며, 실패한 반복도 같은 그룹의 runs에 남는다.

출력의 cases는 원본 경로, sealed/summaryRecomputed/pass/reasons, 전체 summary·창별 분포/큐·snapshot 교집합과 프로세스 누적 CPU/peak RSS·WAL 크기를 포함한다. snapshotCapture/Publish 누적 시간도 서버 생애 관찰이며 측정 구간만의 비용이 아니다. groups는 실제 반복 수·통과 수·qualified와 고유 신규 주문/s 범위·최악 p99를 담는다. 보고서 자신의 build 정보와 기준/참조 입력의 SHA-256도 기록한다.

완료 표식이 없는 실패 실험도 가능한 원본을 재집계하지만 UNSEALED_EXPERIMENT로 탈락한다. 다른 증거 오류나 일부 파일 부재는 cases.reasons에 남기며 누락된 수치를 성공 실적으로 사용하지 않는다. 명령 종료 코드 0은 보고서를 기록했다는 의미이며 성능 통과는 cases.pass/groups.qualified를 확인해야 한다. 전달하지 않은 실험까지 발견해 검증하지는 못하므로 trial 목록과 모든 실패 실행을 함께 보존하고 입력한다. 환경/바이너리 해시는 재현 자료이며 서명된 제3자 검증은 아니다.

## 반복 baseline과 비교 절차

판정 도구와 초기 기준 바이너리를 커밋한 뒤 고정 조건의 반복 측정을 수행한다. 실행 방법은 [개발 기준](../development.md#고정-기준-판정)에 있다. 각 run의 입력·바이너리·환경·hash·지표를 남기고 모든 반복을 함께 보고한다. profile 실행에서 병목 근거를 얻은 뒤에만 P7 변경을 선택한다. 같은 조건에서 다시 측정하며 목표값·불리한 반복을 바꾸거나 숨기지 않는다.

현재 반복 baseline과 P7 비교 수치는 미완료다. 결과는 20초 창에서 관찰한 지속 처리량이며, 영구히 이력을 유지하는 시스템의 무한 시간 처리량으로 일반화하지 않는다. 1,000/s 사이의 미측정 rate나 다른 호스트/시드/계정 집중도에 대한 전역 최대값도 주장하지 않는다. 실제 전원 장애는 별도 장비가 없어 미완료이며 부하 실험이나 프로세스 kill로 대체하지 않는다.
