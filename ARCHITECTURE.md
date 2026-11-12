# Architecture: exchange

## 목적과 현재 상태

학습·포트폴리오용 가상자산 현물 모의 거래소다. 범위와 사용자 관찰 동작은 [제품 명세](docs/product-specs/exchange.md)가 소유한다.

2026-10-29 기준 P4까지 도메인·엔진·Genesis/WAL·runtime·snapshot 생성/복구를 구현했다. P5의 HTTP handler·loopback 서버와 초기화/실행 CLI, 실제 거래·과부하·저장 오류·종료·재시작 테스트를 추가했다. P6 부하 실행·계측·실험 원본 재검증과 [반복 기준 측정](docs/benchmarks/p6-baseline.md)을 완료했다. 정상 조건의 기준 처리량과 snapshot 조건의 목표 미달을 기록했다. [P7 비교](docs/benchmarks/p7-snapshot-sorting.md)의 정렬 이동 후보는 반복 기준 미달로 미채택하고 P6 구현을 유지했다. [스냅샷 처리 한계](docs/benchmarks/p7-snapshot-capacity.md)는 3,000 requests/s를 세 번 채택했다. 실제 전원 장애 검증은 미검증·보류 상태다. [CI](.github/workflows/ci.yml)는 일반 로직과 macOS/APFS 검사를 분리하며, 실행 범위와 원격 확인 절차는 [개발 기준](docs/development.md#ci와-릴리스)을 따른다.

2026-10-30에는 [10분 부하 비교](docs/benchmarks/soak-10m-snapshot-comparison.md), [스냅샷 단계 계측](docs/benchmarks/snapshot-stage-breakdown.md), [복구 디코더 최적화](docs/benchmarks/snapshot-decoder-optimization.md)를 완료했다. decoder 후보는 동일 상태 5쌍에서 전체 복구 중앙값 32.5% 감소와 상태 bytes 일치를 확인해 채택했다. 생성 비용과 owner 사본 생성·동시 부하 지연은 별도 검증 대상으로 구분했다.

2026-11-02에는 [스냅샷 생성 사전 계산](docs/benchmarks/snapshot-preflight-optimization.md)에서 중복 직렬화를 제거했다. 동일 상태 5쌍의 생성 중앙값은 29.7%, 해당 크기 검사 단계는 79.4% 감소했다. 2회차 후보의 지연 증가도 포함했으며 저장 bytes·재시작·양방향 버전 호환을 확인했다. owner 복사/정렬과 실제 writer·sync 계약은 유지했다. 고정 상태 비교만으로 동시 주문 p99·처리량의 개선을 주장하지 않는다.

같은 날 [최적화 버전 10분 부하·복구 비교](docs/benchmarks/optimized-soak-10m-comparison.md)를 수행했다. 끔/켬 모두 전체 복구는 일치했고 켬의 CPU·평균 snapshot 생성·복구 시간은 줄었다. 전체 요청 p99는 두 조건 모두 이전보다 높았고 켬은 생성기 미전송 9건으로 measurementValid=false였다. 두 조건 모두 누락 0·5초 구간별 20ms 참조 기준을 함께 만족하지 못했다.

## 현재 문서 지도

| 실제 경로 | 책임 |
| --- | --- |
| [README.md](README.md) | 프로젝트 소개와 시작 문서 연결 |
| [AGENTS.md](AGENTS.md) | 작업 진입점과 공통 원칙 |
| [docs/index.md](docs/index.md) | 문서 지도·템플릿 적용 이력 |
| [docs/development.md](docs/development.md) | 실제 도구·명령의 구성 상태와 개발 기준 |
| [docs/workflow.md](docs/workflow.md) | 승인·검증·완료 기준 |
| [docs/product-specs/](docs/product-specs/index.md) | 제품 범위와 동작 계약 |
| [docs/design-docs/](docs/design-docs/index.md) | 구현 전 합의한 상태·동시성·저장·복구 계약 |
| [docs/exec-plans/](docs/exec-plans/index.md) | 구현 단계·진행·실제 검증 기록 |

## 현재 개발 도구

| 실제 경로 | 책임 |
| --- | --- |
| [go.mod](go.mod), [.go-version](.go-version) | 모듈 github.com/gaon000/exchange, Go 1.27.3 기준 |
| [scripts/setup-go.sh](scripts/setup-go.sh), [scripts/go](scripts/go) | 검증된 로컬 toolchain 설치와 실행·캐시 격리 |
| [.github/workflows/ci.yml](.github/workflows/ci.yml), [scripts/ci.sh](scripts/ci.sh), [tools/civerify](tools/civerify/main.go) | 일반/실제 저장 CI, 로컬 공용 검사, 선택된 테스트와 실행 결과 대조. 거래 runtime에서 사용하지 않음 |
| [tools/diagnostics](tools/diagnostics/experiment.py), [tools/timerprobe](tools/timerprobe/main.go), [tools/experimentcheck](tools/experimentcheck/main.go) | 새 실행·전체 복구·원본 재집계·trace/OS I/O·타이머 진단·[현재 기준 반복](docs/controlled-baseline.md)과 [기준·후보 비교](docs/experiment-comparison.md). [명령과 경계](docs/diagnostic-tools.md)를 따르며 runtime 의존 경로에 포함하지 않음 |
| [tools/storageprobe/](tools/storageprobe/main.go) | macOS strict sync·잠금·임시 파일 정리 검증. 거래 저장소 구현이 아님 |
| [tools/benchreport](tools/benchreport/main.go) | 완료/실패 실험 원본의 오프라인 재집계·고정 조건·독립 반복 판정. 서버/생성기를 실행하지 않음 |
| [config/examples/local.json](config/examples/local.json) | 초기 잔고·단위 fixture. 도메인 검증과 WAL의 엄격한 Config decoder를 CLI init에 연결 |

## 합의한 구현 목표

Go 단일 프로세스에서 단일 거래쌍을 처리한다. 호가창·잔고·주문·중복 결과의 업무 상태는 한 고루틴만 변경한다. API 수신, 로그 I/O, 불변 스냅샷 사본의 저장은 별도 고루틴에서 수행할 수 있다. 단일 상태 변경 고루틴은 서버 전체를 단일 OS 스레드로 제한한다는 의미가 아니다.

기본 경로는 `형식 검사 → bounded queue → 순번 → 그룹 로그 영구 저장 → 순차 상태 적용 → 결과 응답`이다. 세부 순서·조회 일관성·오류 경계는 [상세 설계](docs/design-docs/exchange-v1.md)를 기준으로 한다.

## 모듈과 의존 방향

P4 모듈과 P5의 api·서버 CLI, P6의 bench·부하 CLI를 구현했다. 아래에서 나머지 예정 경로와 구분한다.

| 경로·상태 | 책임 | 허용 의존 |
| --- | --- | --- |
| [internal/domain](internal/domain/types.go) (구현) | 정수 파싱·산술·ID·Config·명령·결과·버전 타입 | 표준 라이브러리, 외부 I/O 없음 |
| [internal/engine](internal/engine/engine.go) (구현) | 가격 정렬+FIFO, 잔고·주문·체결·최초 결과를 결정적으로 변경 | domain; checkpoint 단계 진단에만 telemetry |
| [internal/strictjson](internal/strictjson/strict.go) (구현) | 중복 키·정확한 필드명·필수 필드·깊이 검사 | 표준 라이브러리 |
| [internal/durable](internal/durable/darwin.go) (구현) | APFS 확인·strict F_FULLFSYNC·FD 수명에 묶인 flock·exclusive rename | 시스템 cgo, 표준 라이브러리 |
| [internal/wal](internal/wal/store.go) (구현) | Genesis·WAL 프레임/검사·Init/Open/Replay/ConfirmRecovery/Append | domain, strictjson, durable, telemetry; engine 의존은 테스트만 |
| [internal/snapshot](internal/snapshot/codec.go) (구현) | envelope 스트리밍·파일 sync·exclusive 게시·정식 후보 열거/검증/재확인 | domain, durable, telemetry, 표준 파일 I/O; strictjson·engine·wal 의존은 테스트만 |
| [internal/runtime](internal/runtime/runtime.go) (구현) | bounded command/query 큐·배치·단일 owner·WAL/snapshot worker·snapshot 후보 선택/대체 복구·두 저장 worker 종료 합류 | engine, wal, snapshot, telemetry |
| [internal/api](internal/api/server.go) (구현) | 엄격한 요청·계정 문맥·응답 DTO·조회·HTTP 한도·loopback 전송 | domain, strictjson, runtime의 command/query 인터페이스, telemetry, 표준 HTTP |
| [cmd/exchange](cmd/exchange/main.go) (구현) | init/serve·설정 검증·모듈 조립·기동 보고·signal 종료와 HTTP/runtime 합류 | 위 모듈의 공개 진입점 |
| [cmd/demo](cmd/demo/main.go), [scripts/demo.sh](scripts/demo.sh) (구현) | 한 명령 도구 준비/빌드, 공개 HTTP로 거래 규칙/ID 충돌과 WAL·snapshot·소유 서버 SIGKILL 복구·최초 응답 확인 | domain, api의 공개 DTO, strictjson, durable의 환경 확인, 표준 라이브러리. engine/runtime 직접 호출 없음 |
| [internal/bench](internal/bench/plan.go) (구현) | 고정 seed 입력·open-loop 실행·초기 상태 확인·공개 HTTP 전송·표본/고유 결과 집계 | domain, strictjson, 표준 라이브러리; engine/runtime 직접 호출 없음 |
| [cmd/loadgen](cmd/loadgen/command.go) (구현) | 설정/ready 보고서 대조·공개 HTTP 부하·측정 원본/완료/실패 파일 | bench, buildinfo, strictjson, telemetry, wal의 설정 해시 계산; run 파일 접근 없음 |
| [cmd/experiment](cmd/experiment/command.go) (구현) | 새 서버/생성기 프로세스 수명·환경 수집·복사 바이너리/설정·원본 해시 묶음 | bench 입력 타입, strictjson, wal 설정 decoder, buildinfo, 표준 파일/프로세스/OS 읽기; 엔진 직접 호출 없음 |
| [internal/buildinfo](internal/buildinfo/info.go) (구현) | 실행 바이너리의 Go/플랫폼·VCS revision/dirty 정보 | 표준 라이브러리 |
| [internal/telemetry](internal/telemetry/histogram.go) (구현) | 고정 크기 지연 히스토그램·코드에 고정한 단계별 시간·Go/OS 프로세스 자원 관찰 | 표준 라이브러리·OS 읽기 호출; 엔진/저장 상태 접근 없음 |

API는 엔진 내부 맵을 직접 수정하지 않는다. 엔진의 거래 판단과 상태 전이는 HTTP·파일·시각·난수·외부 서비스에 의존하지 않는다. checkpoint의 선택적 단계 계측만 단조 시계로 시간을 관찰하며 그 값을 거래·복구 판단에 사용하지 않는다. WAL은 거래 승인 여부를 결정하지 않는다. loadgen의 전체 경로 측정은 엔진 직접 호출을 우회 경로로 사용하지 않는다. 공통 타입은 실제 공유 계약에 한해 분리하고 순환 의존은 허용하지 않는다.

P1의 engine은 자체 고루틴·잠금 없이 한 소유자가 순차 호출한다. New/Lookup/Apply와 값 복사 조회를 제공하며, State는 안정된 순서의 진단용 사본이다. State를 snapshot 파일 형식으로 직접 직렬화하지 않는다. P4의 [Capture/Restore](internal/engine/checkpoint.go)는 별도 [Checkpoint DTO](internal/domain/checkpoint.go)를 복사하고, 과거 매칭을 다시 수행하지 않고 검증된 상태를 직접 구성한다. [Runtime.Snapshot](internal/runtime/snapshot.go)은 owner 경계에서 한 사본만 만들고 별도 worker가 게시한다. WAL·snapshot worker가 모두 종료한 뒤 잠금을 해제한다. 후보 선택·WAL 대조·대체 복구는 runtime.Open이 담당한다. 자료구조·순번·오류 경계의 구체적인 연결 계약은 [상세 설계](docs/design-docs/exchange-v1.md#p1-구현과-호출-계약)와 [스냅샷 계약](docs/design-docs/storage-v1.md#5-snapshot-dto와-게시)을 따른다.

## 시스템과 데이터 경계

- 메모리의 업무 상태는 runtime이 소유한 engine이 변경한다. 조회에는 명령 경계에서 만든 사본 또는 안전하게 게시된 불변 결과를 제공한다.
- 재구성의 기준은 영구 저장된 초기 설정·명령 로그와 검증된 스냅샷이다. 데이터 디렉터리는 프로세스 하나만 소유한다.
- 일반 재시작과 새 실험 초기화를 분리한다. `runId`와 설정·버전을 함께 검증한다.
- 전원 장애 내구성은 대상 OS·파일 시스템·장치의 동기화 계약이 전제다. 프로세스 종료 검증과 전원 장애 검증을 구분한다.
- v1에는 실제 거래소·은행·외부 시세 연동이 없다. 프로그램 경계는 로컬 테스트 클라이언트와 영구 파일 저장소다.

## 변경 관리

모듈 책임·의존 방향은 이 문서, 거래 규칙은 제품 명세, 상태·저장 계약은 상세 설계, 진행·검증 증거는 [구현 계획](docs/exec-plans/active/20261029-1531-exchange-v1.md)에서 관리한다. 구현 경로가 생기면 현재 상태와 예정 경로를 구분해 갱신한다. 완료 계획의 과거 검증을 현재 동작의 증거로 재사용하지 않는다.
