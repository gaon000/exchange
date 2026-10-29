# 부하 생성과 측정 v1

상태: Accepted (P6 구현 계약)
결정일: 2026-10-29 (Asia/Seoul)
구현 상태: [internal/bench](../../internal/bench/plan.go)의 입력·실행·집계와 [loadgen CLI](../../cmd/loadgen/command.go)의 원본 출력, 서버 구간·프로세스 자원 계측을 구현했다. 스냅샷/프로파일 서버 옵션과 [experiment CLI](../../cmd/experiment/command.go)의 환경/바이너리/원본 묶음을 연결했다. [benchreport](../../tools/benchreport/main.go)로 원본을 재검증한 [P6 기준 측정](../benchmarks/p6-baseline.md)을 완료했다. 이 문서는 지표 정의를 소유하며 성능 목표값과 실측 결과는 기준 보고서에서 관리한다.

## 목적과 경계

[제품 명세](../product-specs/exchange.md#성능-결과의-판정)의 영구 저장을 포함한 성능을 측정한다. 측정 대상은 공개 HTTP API이며 부하 생성기가 엔진을 직접 호출하지 않는다. 새 실험 초기화와 기존 run 재시작은 [저장 계약](storage-v1.md#1-데이터-디렉터리와-초기화)을 따른다. bench 핵심 라이브러리는 파일·저장소·서버를 초기화하지 않는다.

CLI는 서버의 ready 보고서에서 runId·configHash·listen 주소·서버 설정과 바이너리 build 정보를 읽는다. 같은 초기 설정의 canonical hash를 대조한 뒤 모든 등록 계정의 공개 잔고와 asOfSeq=0을 확인한다. 기존 실험을 재초기화하지 않는다. 실제 HTTP 전송은 숫자 loopback에 한정하며 proxy·redirect·자동 업무 재시도를 사용하지 않는다. 다른 클라이언트가 동시에 쓰지 않는 전용 실험 run이 전제이며 사전 검사가 이후 외부 쓰기까지 잠그지는 않는다.

## 재현 입력

`Build(runId, Spec)`는 설정을 검증한 뒤 준비 주문과 워밍업/측정 요청을 모두 생성한다. 본문 JSON 인코딩은 측정 시작 전에 끝낸다. Version=1, runId, Spec, 순서가 고정된 모든 요청을 JSON으로 연결한 SHA-256을 보관한다. namespace·계정 순서·seed·가격/수량·비율·요청률·시간 중 하나라도 달라지면 다른 입력이다. Go toolchain과 소스 revision도 최종 실험 원본에 기록해야 한다.

- 거래 계정은 2~10,000개다. 새 일반 주문의 첫 계정 집중도를 설정할 수 있다. 취소와 중복은 원래 명령의 계정을 사용한다. 등록·초기 잔고의 실제 검사는 후속 실행 단계의 책임이다.
- 양쪽 가격 level 1~100개에 한 주문씩 준비한다. 가격은 중심값에서 tick 간격만큼 떨어져 있으며 서로 교차하지 않는다. 준비 주문은 명령 한도에 포함하지만 워밍업/측정 시계 밖에서 완료해야 한다.
- 입력 비율은 Rest·Marketable·Cancel·Duplicate·RejectProbe의 정수 가중치 합 100이다. seed에 따른 선택 비율이며 유한 표본의 정확한 건수나 실제 체결 비율을 뜻하지 않는다. Marketable은 초기 호가 범위를 넘는 가격이다. 실행 중 호가가 바뀌면 실제 결과는 달라질 수 있다.
- 취소는 이전에 계획한 같은 계정 주문을 참조한다. 응답을 기다려 대상을 고르지 않으므로 대상보다 먼저 도착하거나 이미 체결된 주문을 취소할 수 있다. 성공 취소와 no-op 결과를 구분한다.
- 중복은 이전 원본 명령의 키·내용·최초 phase/예정 시각을 그대로 보존한다. 같은 키를 새 명령으로 세지 않는다. RejectProbe는 거래 계정과 분리된, 초기 잔고 0인 등록 계정을 사용한다. 준비 단계에서 이 조건을 확인해야 한다.
- 준비를 제외한 워밍업+측정 요청은 최대 2,000,000개, 두 구간 합계는 최대 1시간이다. 요청률은 초당 1~1,000,000이다. 이는 생성기 자원 상한이며 권장 부하나 성능 목표가 아니다. `UniqueCommands`는 준비·업무 거절을 포함하고 중복을 제외한 필요한 명령 한도다.

Plan의 접근자는 사본을 반환한다. 입력·결과를 유한 배열로 보존하며 생성기 자체 메모리 비용도 후속 실험에서 기록한다. 잘못된 설정을 사용한 실행이나 준비 주문 실패를 유효한 기준 실험으로 취급하지 않는다.

## 예정 시각과 포화

각 구간의 요청 `i`는 `구간 시작 + floor(i × 1초 / 요청률)`에 예정된다. 나눗셈 몫·나머지로 계산하므로 잘린 단일 interval을 반복해서 더하는 오차가 누적되지 않는다. 구간 길이×요청률에 소수 부분이 있으면 구간 안에 위치하는 마지막 요청까지 포함한다. 측정은 워밍업의 고정된 종료 시각에서 시작한다.

고정 수의 실행 worker에 즉시 전달할 수 있을 때만 요청을 실행한다. 생성기 내부 대기열은 없다. 응답을 기다려 다음 예정 시각을 뒤로 밀지 않으며, 늦어진 요청을 무한한 따라잡기 burst로 보내지도 않는다.

| 관찰 | 기록 |
| --- | --- |
| 실행 worker가 모두 사용 중 | `GENERATOR_CAPACITY`, 전송하지 않음 |
| 예정 시각보다 MaxLateness를 넘김 | `GENERATOR_LATE`, 전송하지 않음 |
| 아직 전송하지 않은 상태에서 실행 취소 | `GENERATOR_CANCELED` |
| 이미 executor에 전달한 요청의 timeout/연결 오류 | 결과 `UNKNOWN`; 서버 명령 취소로 해석하지 않음 |
| executor panic | 원본 표본 유지, 실행 실패·측정 무효 |

worker로 전달된 뒤 CPU 실행이 지연된 경우도 전송 전에 lateness를 다시 검사한다. 늦음·포화·취소를 포함해 계획한 인덱스마다 표본 하나가 남는다. 정상 실행은 마지막 응답이 빨라도 고정된 측정 구간 끝까지 관찰하고, 구간이 끝난 뒤의 응답도 요청 deadline까지 합류한다. Executor는 context를 준수해야 한다.

## 시간과 분모

실행 시계는 monotonic 차이를 사용하고 절대 시각은 UTC로 따로 남긴다. 예정 시각, executor 시작, 전송 완료(관찰할 수 있을 때), 완전한 응답/오류 관찰을 구분한다. 시작/전송을 관찰하지 못한 시각은 -1이며 지연 0으로 바꾸지 않는다. 시계 시작 전 취소의 FinishedAt은 0 경계로 표시하고 실제 FinishedUTC도 남긴다. 시간·지연의 JSON 단위는 나노초다.

- SchedulerDelay: 실제 시작에서 예정 시각을 뺀 값. 전송하지 않은 포화/지연도 관찰 시각에서 예정 시각을 뺀 값을 포함한다. 미래 요청의 취소는 이 분포에서 제외한다.
- RequestLatency: 시작부터 완전한 응답/오류까지다. 성공만 추려 전체 요청 p99를 낮추지 않는다. 입력 의도별·응답 분류별 분포도 함께 제공한다. timeout의 시간은 실패 관찰까지의 대기이며 실제 서버 처리 완료 지연을 뜻하지 않는다. UNKNOWN 수·실패율과 함께 읽는다.
- ScheduledAttemptLatency: 개별 시도의 예정 시각부터 응답/오류까지다. TransportWriteDelay는 관찰 가능한 전송 완료와 시작의 차이다.
- UniqueNewOrderScheduledLatency: 고유 승인 주문의 **원래 예정 시각**부터 가장 먼저 관찰한 완전한 성공까지다. 원본보다 중복 응답이 먼저 끝나도 원래 예정 시각을 유지한다. 워밍업 원본의 측정 구간 재시도는 측정 신규 주문에 들어가지 않는다.
- 분포는 실제 표본을 정렬한 nearest-rank p50/p95/p99·최소·최대다. Count=0이면 관찰이 없다는 뜻이며 0ns 성능으로 해석하지 않는다.

고유 결과는 계정·명령 종류·요청 ID와 runId로 묶는다. 같은 키의 최초 결과·순번이 달라지거나 서로 다른 키가 같은 순번을 주장하면 집계를 거절한다. 입력 의도와 실제 응답 code·체결 수를 구분하며, 업무 거절·성공 취소·취소 no-op·중복·과부하·서버/전송 timeout을 신규 주문 성공에 더하지 않는다.

처리량의 분모는 고정된 측정 구간 길이다. 해당 구간에서 새로 예정한 고유 명령 중 구간 안에 완전한 결과가 관찰된 신규 승인 주문·성공 취소·체결 수를 각각 초당 수로 표시한다. 구간 후 drain에서 확인된 성공은 전체 cohort 결과와 지연 분포에는 남지만 구간 안 처리량을 늘리지 않는다. UNKNOWN 논리 명령은 이후 완전한 중복 결과가 관찰되면 해소하고 그렇지 않으면 남긴다.

생성기 누락률의 분모는 예정 요청 수, timeout/과부하 비율의 분모는 실제 시작 요청 수다. 각 건수도 함께 남긴다. 워밍업 또는 측정에서 생성기 누락·잘못된 응답·실행 중단이 있으면 `MeasurementValid=false`다. 서버의 과부하/timeout은 측정한 실패율이며 그 자체로 데이터를 숨기지 않는다. `MeasurementValid=true`도 p99·허용 실패율 목표 통과나 최대 지속 처리량 달성을 뜻하지 않는다.

## HTTP 전송과 실행 파일

- [HTTP executor](../../internal/bench/http.go)는 deadline·64 KiB 응답 본문·8 KiB 헤더 상한을 사용한다. 완전한 JSON의 버전·run·계정·키·순번·주문 수량/상태와 원래 입력을 검사하고, 최초 응답 bytes의 SHA-256을 남긴다. 잘린 200이나 연결 오류는 UNKNOWN, 잘못된 JSON/상태는 InvalidResponse다. 응답 본문 전체를 표본에 복제하지 않으며 code·순번·체결 수·상태·fingerprint와 관찰 시각을 보존한다.
- httptrace의 WroteRequest를 실제 쓰기 완료 관찰로 기록한다. 실패 후 늦게 실행되는 callback은 반환된 표본을 변경하지 않는다. connection pool 한도는 실행 worker 한도와 같고 전송 context가 응답 본문 읽기까지 제한한다.
- [준비 단계](../../internal/bench/prepare.go)는 계획의 고유 명령 수가 설정 한도 이내인지, 모든 계정이 등록됐는지, 거절 probe 계정이 무잔고인지, seed 주문 자금이 충분한지 먼저 검사한다. 공개 잔고 검증 뒤 준비 주문을 순차 실행하고 1부터 이어지는 순번·OPEN·체결 0을 확인한다. 준비 실패는 재시도하지 않고 부분 보고서와 서버 상태를 보존한다.
- loadgen은 `--config`, `--workload`, `--server-report`, `--output`을 필수로 받는다. 입력은 4 MiB 이하 일반 파일과 엄격한 JSON이다. 출력은 기존 parent 아래 새 디렉터리만 허용하며 기존 파일을 덮어쓰지 않는다. 실행 예시는 [개발 기준](../development.md#http-부하-실행)에 있다.

| 출력 | 내용 |
| --- | --- |
| manifest.json | 원래 설정·초기 공급량·서버 ready 보고서·서버/생성기 Go/OS/CPU 아키텍처와 소스 revision·dirty 여부·plan digest |
| plan.jsonl | 준비/워밍업/측정의 명령·본문·예정 시각·원래 phase, 한 줄당 요청 하나 |
| preparation.json | 조회한 초기 잔고·실제 준비 시도의 시각/결과, 부분 실패도 보존 |
| samples.jsonl | 모든 예정 인덱스의 실제 시각·응답 요약 또는 생성기 누락 원인 |
| run.json | UTC 기준 시각·monotonic 경과 시간·실행 옵션·중단 여부·생성기 프로세스의 Run 전후 자원 관찰 |
| summary.json | 원본 표본에서 계산한 지연·처리량·실패율과 측정 유효성 |
| completed.json | 앞 여섯 파일의 SHA-256과 plan digest. 파일 쓰기/Sync/close 후 임시 표식을 rename하며 성능 목표 통과를 뜻하지 않음 |
| failed.json | 준비/실행/출력/집계 실패 원인. 실패 시 남은 파일은 지우지 않음 |

측정 중 파일 쓰기는 하지 않는다. SIGINT/SIGTERM은 전송 context를 취소하고 worker 합류 후 미래 요청까지 취소 표본으로 남긴다. 중단·생성기 누락·잘못된 응답은 종료 코드 1이며 completed.json을 만들지 않는다. 서버 과부하/timeout만 발생한 유효 표본은 파일을 완료할 수 있으며 허용 실패율 판정은 후속 성능 기준의 책임이다. 강제 kill이나 디스크 실패에서는 일부 파일/임시 표식만 남을 수 있다. 완료 JSON과 해시를 확인해야 하며 이 출력 방식은 거래 WAL의 strict sync나 전원 장애 내구성 보장이 아니다.

빌드 정보가 없는 바이너리는 revisionKnown=false로 기록하며 현재 checkout의 Git 값으로 대신하지 않는다. 수정 중인 바이너리는 modified=true다. 이 정보만으로 CPU 모델·RAM·파일 시스템·서버 구간 비용을 측정했다고 주장하지 않는다.

## 서버 구간과 자원 계측

[telemetry](../../internal/telemetry/histogram.go)는 고정 크기 히스토그램을 사용한다. 경계는 0과 1/2/5×10ⁿ ns, 마지막은 MaxInt64다. 빈 bucket은 출력에서 생략한다. 서버 분위수는 해당 bucket 상한으로 근사하며 클라이언트의 정확한 p99와 구분한다. count·sum·min/max·bucket count는 정수 문자열이다. 합계 overflow는 포화 값과 flag로, 음수 입력은 별도 건수로 기록한다.

각 히스토그램의 사본은 일관되지만 여러 지표를 동시에 고정하지는 않는다. JSONL의 startedUTC~finishedUTC가 수집 구간이다. 누적 카운터는 해당 서버 프로세스 기동 이후 값이며 과거 명령 재생을 다시 세지 않는다. reset이나 계정/주문별 label은 없다.

| 지표 | 관찰 경계와 분모 |
| --- | --- |
| commandQueueWait / queryQueueWait | 접수 직전부터 owner가 최초로 꺼낼 때까지. 다음 배치 이월 명령을 두 번 세지 않음 |
| batchWait | 고유 명령의 batch 편입부터 WAL worker로 전달하기 직전까지, 고유 명령당 하나 |
| append | WAL worker의 Append 전체. 검증·인코딩·write/sync·후처리를 포함하며 저장 실패 시간도 기록 |
| WAL encode / write / sync | 실제 인코딩, short-write/EINTR 재시도를 포함한 writeAll, strict sync 호출. 초기화·복구 동기화 제외. 실패한 단계도 기록 |
| apply / query | owner의 엔진 Apply 한 번 / 조회 한 번. 실패한 Apply와 정상 적용 수는 별도 |
| snapshotCapture / snapshotPublish | owner의 성공한 사본 생성 / worker 게시 시도 전체. 게시 실패와 owner가 확인한 완료 수 별도 |
| HTTP handler / encode / write | 전체 handler / JSON 인코딩 / WriteHeader부터 Write와 최종 Flush까지. health·조회·거절 응답도 포함 |

배치 수·고유 명령 수·payload 합계/최대 개수, 접수·캐시/대기 중복·적용·fatal 실패·snapshot 완료/오류도 기록한다. 큐 현재 길이는 관찰 시점 값이며 observedCommandQueuePeak는 접수 직후 표본의 최대값이다. 빠른 dequeue를 놓칠 수 있어 진짜 최대 점유량과 구분한다. WAL encodedBytes는 시도한 frame 크기이므로 부분 쓰기 실패가 있으면 파일 증가량과 다르다. WAL groups/commands는 sync 성공 수이며 이후 Seek·owner 확인·엔진 적용과 구분한다. ioErrors는 Write/Sync 오류 수다.

HTTP 상태/bytes는 서버가 시도하고 Write에서 반환받은 값이다. Flush 실패에도 2xx와 일부 bytes가 관찰될 수 있으므로 클라이언트 성공 수로 쓰지 않는다. 서로 다른 분모·중첩 구간의 시간을 그대로 더해 요청 지연으로 만들지 않는다.

오류를 반환한 WAL 단계는 시간 표본을 남긴다. panic으로 중단된 세부 단계는 표본이 없을 수 있으며 worker에서 복구한 Append 전체 시간과 owner fatal 실패 수를 함께 확인한다. 성공한 capture 시간만 기록하므로 snapshot 복사 중 panic도 완료 표본으로 보지 않는다.

`exchange serve --metrics-file <새 파일> --metrics-interval 1s`는 initial·periodic·final JSONL을 별도 worker에서 기록한다. 주기는 100ms~1m다. 파일 출력 기본값은 꺼짐이며 구간 계측은 항상 켜져 있다. ready의 metrics.enabled는 파일 출력 여부, instrumentation=true는 구간 계측 여부다. 파일 충돌은 복구 전에 거절한다. 수집/출력 실패는 일반 종료를 요청하고 접수된 명령과 HTTP를 drain한 뒤 오류를 반환한다. final은 drain 뒤 수집하며 기록 성공·정상 종료와 함께 확인한다. 파일 Sync는 종료 때 수행하며 거래 WAL의 전원 장애 보장이 아니다.

[프로세스 관찰](../../internal/telemetry/process.go)은 현재 프로세스의 OS user/system CPU 누적 시간·peak RSS와 Go heap objects/live heap/전체 관리 메모리·GC 횟수/CPU/멈춤 분포·goroutine 수를 기록한다. Go runtime/metrics를 읽으며 강제 GC나 ReadMemStats를 호출하지 않는다. peak RSS는 프로세스 생애 최대값이고 현재 RSS가 아니다. Go 관리 메모리는 resident memory가 아니며 GC CPU는 Go의 추정치다. 지원 불가 지표는 unavailable로 표시한다. WALBytes는 실제 commands.wal 크기다.

loadgen run.json에는 Plan/준비 완료 후 Run 전·후의 생성기 자원 관찰을 추가한다. worker 준비·워밍업·측정·drain을 포함하며 측정 phase만의 CPU가 아니다. 두 시점 heap 차이는 최대 메모리나 총할당이 아니다. 생성기와 서버 자원을 구분한다.

주기 수집·JSON 출력·계측 비용도 실험에 포함된다. 정상·snapshot·profiling 비교에서 같은 계측 설정을 유지한다. 서버 JSONL은 loadgen completed.json의 6개 파일과 별도이며 서버 종료 후 final까지 확보한다. 환경/원본 묶음은 아래 실험 실행기로 연결하며 반복 기준 측정은 남아 있다.

## 스냅샷과 프로파일 실험 옵션

[서버 실험 옵션](../../cmd/exchange/experiments.go)은 기본 비활성이다. ready 보고서의 experiments는 실제 snapshotInterval·snapshotLog·snapshotLimit·profileDir·MemProfileRate를 기록하며 loadgen manifest도 이를 보존한다. 모든 출력 파일/디렉터리는 복구 전에 새 경로로 확보한다. 기존 경로와 충돌하면 WAL 복구를 시작하지 않고, 생성하다 실패한 부분 파일도 보존한다.

`--snapshot-interval`과 `--snapshot-log`는 함께 지정한다. 주기는 100ms~1h이며 서버 시작 시점에 맞춰 첫 주기 후 요청한다. 앞 게시가 끝나야 다음 요청을 하고 지나간 주기는 skippedIntervals로 남겨 누적 대기열을 만들지 않는다. 최대 1,000회 이후에는 서버를 정상 drain하고 실험 실패로 종료한다. 각 JSONL에 예정·호출 시작·응답 시각, runId·snapshotSeq·outcome·오류를 기록한다. 단순히 옵션을 켰다는 사실과 측정 구간에서 완료한 snapshot을 구분하며 실제 시각의 교집합으로 확인한다.

서버 종료는 주기 worker의 대기를 취소한다. 이미 접수된 게시 작업은 runtime 종료가 끝까지 기다린다. 따라서 취소된 waiter가 기록한 UNKNOWN은 게시 실패나 파일 부재의 증거가 아니다. 예상하지 않은 snapshot 실패·로그 출력 실패는 서버 종료를 요청하고 오류를 반환한다. 파일 Sync·Close 결과도 종료 성공 판정에 포함한다.

`--profile-dir`은 cpu.pprof·heap.pprof·allocs.pprof·profile.json을 생성한다. CPU 수집은 복구 후 HTTP 준비 전부터 HTTP/runtime drain 후까지여서 준비·워밍업·측정·drain을 모두 포함한다. CPU 수집을 종료한 뒤 강제 GC 한 번으로 종료 시 live heap을 관찰하고, heap/allocs를 기록한다. 엔진 상태는 이 시점까지 유지한다. allocation profile은 현재 MemProfileRate에 따른 표본이며 모든 할당의 정확한 로그가 아니다. profile.json은 수집 시작·CPU 종료·파일 기록 시각, 표본율과 postDrainGC를 기록한다.

CPU profile의 비동기 쓰기 실패도 보존해 일반 종료를 요청한다. 모든 profile을 Sync·Close하고 마지막 metrics를 남기므로 final 프로세스 CPU/GC에는 종료 시 강제 GC와 profile 기록 비용도 포함된다. 이는 측정 구간만의 비용이 아니다. 정상 성능 실행과 profile 실행을 구분하고, 두 실행에서 snapshot·주기 계측 설정을 같게 유지한다. 프로파일 없이도 Go의 기본 메모리 표본 수집은 켜져 있을 수 있으며 실제 표본율을 함께 기록한다. 전원 장애 내구성은 이 관찰 파일의 보장 범위가 아니다.

## 재현 실험 실행기와 원본 묶음

[experiment CLI](../../cmd/experiment/command.go)는 지원 환경인 로컬 macOS/APFS에서 새 디렉터리를 만들고 서버 init→serve→loadgen→서버 종료를 실행한다. 기존 run이나 출력 디렉터리를 재사용하지 않는다. 서버/생성기 바이너리와 초기 설정·workload를 복사하고, 복사본을 실행한다. 일반 파일만 복사하며 설정은 4 MiB, 각 바이너리는 128 MiB 이하로 제한한다. 외부 production dependency는 없다.

`--scenario`는 normal·snapshot·profile·snapshot-profile이다. 기본 주기는 metrics 1s·snapshot 5s이며 모든 실제 인자를 manifest.json에 기록한다. `--server-arg=--batch-delay=2ms`처럼 runtime/HTTP 옵션만 반복 지정할 수 있고 같은 옵션 중복이나 데이터/포트/기록 경로 재정의는 거절한다. 일반 성능 비교도 같은 metrics 설정을 유지한다.

환경은 거래 시작 전에 읽는다. environment.json은 runner 바이너리의 build 정보, 논리 CPU 수, runner GOMAXPROCS, 명시된 GOMAXPROCS/GOGC/GOMEMLIMIT/GODEBUG, CPU 모델·RAM·장치 모델·macOS 버전, 파일 시스템 종류/로컬 여부·allocation block·당시 가용 bytes를 포함한다. 시스템 명령 조회가 실패하면 해당 항목에 오류를 남긴다. 전체 환경변수나 자격 증명은 수집하지 않는다. 서버/생성기의 build 정보는 각각 ready와 loadgen manifest에 별도로 남는다. 가용 공간은 초기 관찰이며 실행 중 공간 보장이 아니다.

| 묶음 경로 | 내용 |
| --- | --- |
| bin/exchange, bin/loadgen | 실제 사용한 복사 바이너리 |
| config.json, workload.json | 복사 후 다시 검증한 입력 bytes |
| environment.json, manifest.json | 실행 환경·시나리오·단계별 실제 argv |
| initialized.json, server-ready.json | 초기화와 복구/서버 설정 보고 |
| init.stderr, server.stderr, loadgen.stdout, loadgen.stderr | 직접 파일로 연결한 자식 출력 |
| server-metrics.jsonl | 서버 initial~final 지표 |
| snapshot-events.jsonl, profiles/ | 켠 시나리오에서만 생성하는 snapshot 이벤트·CPU/heap/allocs/metadata |
| loadgen/ | 앞서 정의한 전체 입력·표본·집계·완료/실패 파일 |
| run/ | 해당 실험의 Genesis·WAL·snapshot·잠금 파일. 거래 이력을 임의 삭제하지 않음 |
| verification.json | run/plan identity, 측정 시간, 계측 수와 측정에 겹친 완료 snapshot 수 |
| completed.json | 자식 정상 종료·검증 후 전체 일반 파일의 SHA-256. 이 표식 자체는 해시 목록에서 제외 |
| failed.json | 실패 원인/시각. 부분 결과를 보존하며 completed를 만들지 않음 |

서버 준비에는 30초, 초기화에는 30초, 부하 생성기에는 준비 주문 deadline 합+워밍업+측정+2분의 한도를 둔다. SIGINT/SIGTERM 또는 실패 시 자신이 만든 자식만 SIGTERM으로 종료하고 30초 동안 합류한다. 제한을 넘으면 해당 자식만 kill하고 실험을 실패 처리한다. 개발 컴퓨터나 다른 서버를 종료하지 않는다. 서버의 저장 drain에 실패한 실행을 정상으로 보지 않는다.

완료 전에 loadgen의 정해진 6개 파일과 완료 해시, summary의 유효성·plan digest·run/config identity·시간 범위, 서버 initial/final을 확인한다. snapshot 시나리오는 실제 완료 이벤트의 시작~끝과 측정 구간이 겹쳐야 한다. UNKNOWN 이벤트는 성공 건수에 포함하지 않으며 서버가 너무 빨리 끝나 snapshot이 없으면 설정을 켰더라도 실패다. profile 시나리오는 수집 시각이 Run 전체를 덮는지와 압축 파일 무결성을 검사한다. 이는 protobuf 전체 의미 검증을 대체하지 않으며 별도 `go tool pprof` 해석 증거를 기록한다.

마지막으로 symlink/특수 파일을 거절하고 최대 1,200개 파일의 해시를 계산해 임시 완료 표식을 rename한다. 파일 Sync/Close와 자식 exit 성공을 요구하지만 거래 WAL의 strict sync/전원 장애 보장을 관찰 파일에 확대하지 않는다. 강제 kill·디스크 오류에서는 failed.json조차 없을 수 있으므로 완료 표식이 없는 디렉터리는 미완료다. 완료 이후 stdout 출력 실패는 CLI 오류가 되지만 이미 검증한 completed 묶음을 무효화하지 않는다. 완료 묶음을 직접 변경하거나 같은 run을 재사용하면 원래 해시와 달라진다.

실행기의 완료는 재현 자료가 준비됐다는 의미다. p99 상한·허용 실패율·지속 기간/반복 수의 성능 판정과 최대 지속 처리량은 별도 기준 측정에서 결정한다. 한 머신의 생성기와 서버가 자원을 함께 쓰는 한계도 결과에 기록한다.

## 기준 측정과 후속 비교

- 구현한 실행기로 정상·snapshot·profiling을 반복 측정하고 snapshot 시간/메모리를 비교했다. 정상 조건은 세 번 통과했으며 snapshot 조건의 지연·생성기 누락은 실패 원본과 함께 기록했다.
- 초기 탐색 후 [고정 비교 조건 v1](../benchmarks/p6-baseline.md#고정-비교-조건-v1)을 확정했고 판정 도구와 반복 baseline을 연결했다. P7에서 같은 조건으로 비교한다.

현재 [입력 테스트](../../internal/bench/plan_test.go), [스케줄러 테스트](../../internal/bench/runner_test.go), [집계 테스트](../../internal/bench/summary_test.go)는 고정 입력·순서·누락·중복·시간/분모 계산을 검증한다. [전송 테스트](../../internal/bench/http_test.go)는 잘린 응답·deadline·redirect·identity를, [실제 CLI 통합](../../cmd/loadgen/native_test.go)은 HTTP→runtime→APFS 거래와 파일 재집계·중단/재사용 거절을 확인한다. 가짜 executor 결과와 짧은 실제 실행 smoke를 기준 지속 처리량으로 보고하지 않는다. 전체 진행과 실행 증거는 [구현 계획](../exec-plans/active/20261029-1531-exchange-v1.md)에 남긴다.
