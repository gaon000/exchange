# Development

## 환경과 도구

2026-10-29 P0에서 Go 1.27.3을 [.go-version](../.go-version)과 [go.mod](../go.mod)에 기록했다. 모듈은 `github.com/gaon000/exchange`다. 기본 PATH와 확인한 표준 설치 위치에서는 Go를 찾지 못해 공식 배포본을 프로젝트의 `.tools/go1.27.3`에 설치했다. 시스템 Go·셸 설정은 변경하지 않았다.

| 항목 | P0에서 확인한 기준 환경 |
| --- | --- |
| OS/아키텍처 | macOS 15.8 (24H23), Darwin 24.6.0, arm64 |
| CPU/메모리 | Apple M3 Pro, 논리 CPU 11개, RAM 18 GiB |
| 저장소 | 내장 SSD의 로컬 APFS, 4,096-byte allocation block |
| Go/모듈 | Go 1.27.3 darwin/arm64, 외부 require 없음 |
| C toolchain | Apple Command Line Tools의 clang, CGO_ENABLED=1 |
| 검증 범위 | 파일/디렉터리 F_FULLFSYNC·잠금·read-back·probe 테스트. 전원 장애 미검증 |

현재 지원 기준은 이 환경이다. 다른 OS·파일 시스템·Go 버전은 검증 후 확대한다. 하드웨어/가용 디스크 공간은 P6 측정 시작 시 다시 기록한다. [공식 다운로드 목록](https://go.dev/dl/?mode=json)에서 확인한 `go1.27.3.darwin-arm64.tar.gz` SHA-256은 `dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2`다. 설치 스크립트는 압축 해제 전에 이 값을 확인한다.

Go 코드는 Go 모듈·도구로 관리한다. 향후 JavaScript/TypeScript 도구를 도입한다면 기존 사용자 규칙에 따라 Node.js 22 이상과 pnpm을 우선한다. 적절한 Node.js가 없으면 nvm 설치 여부와 사용 가능한 버전을 먼저 확인한다. 현재 Node.js 애플리케이션이나 pnpm 프로젝트를 구성한 것은 아니다.

도구를 도입하면 실제 manifest, lockfile, 버전 설정과 CI를 근거로 정확한 버전을 기록한다. 서로 다르면 차이를 해결하거나 지원 범위를 명시한다.

## 시작하기

[README](../README.md), [작업 안내](../AGENTS.md), [제품 명세](product-specs/exchange.md), [상세 설계](design-docs/exchange-v1.md)를 읽는다. 저장소 루트에서 다음을 실행한다.

```sh
sh scripts/setup-go.sh
scripts/go version
scripts/go test ./...
scripts/go run ./tools/storageprobe -dir .
```

setup은 네트워크로 개발 toolchain만 내려받는다. 이미 같은 버전이 있으면 재설치하지 않는다. 자동 버전 선택 대신 [scripts/go](../scripts/go)가 정확한 버전을 확인하고 `GOTOOLCHAIN=local`, `GOENV=off`, `GOWORK=off`로 실행한다. 캐시와 GOPATH는 `.cache/` 아래다. `.tools/`, `.cache/`, 실험 데이터 `var/`와 빌드 출력 `bin/`은 Git에서 제외한다. [Go toolchain 문서](https://go.dev/doc/toolchain)의 go directive 자체는 최소 버전이므로 재현 실행에는 wrapper를 사용한다.

실행 진입점은 거래 서버 CLI·HTTP 부하 CLI와 개발 probe다. 서버 CLI는 아래 절차로 초기화·기동하며, probe는 `-dir`로 지정한 기존 parent에 고유 임시 디렉터리를 만들고 자기 파일만 정리한다. 실제 실험 데이터에 오류를 주입하는 도구가 아니다.

CLI는 명시적인 flag와 [local.json](../config/examples/local.json)을 사용하며 환경변수 loader는 없다. init은 wal.DecodeConfig로 설정을 검증하고 wal.Init으로 새 Genesis를 저장한다. serve는 저장된 설정으로 runtime.Open을 호출한다. fixture에는 비밀값이 없다. 실제 credential이 필요해지면 안전한 별도 경로로 관리한다.

## 명령

명령은 저장소 루트 기준이다. 다른 작업 경로가 필요하면 해당 행에 명시한다. 실행 가능한 스크립트가 없는 항목을 예시 명령으로 대체하지 않는다.

| 작업 | 실제 명령·구성 상태 |
| --- | --- |
| 개발 toolchain 준비 | `sh scripts/setup-go.sh` (macOS arm64) |
| toolchain 확인 | `scripts/go version`; `scripts/go env GOVERSION GOOS GOARCH CGO_ENABLED CC` |
| Go 의존성 | go.mod만 존재, 외부 require/go.sum 없음. 추가 production dependency는 승인 전 설치하지 않음 |
| 로컬 실행 | `bin/exchange init --config config/examples/local.json --data-dir var/runs/demo`; `bin/exchange serve --data-dir var/runs/demo` (먼저 빌드·parent 준비) |
| 정적 분석 | `scripts/go vet ./...` |
| 포맷 검사 | `.tools/go1.27.3/bin/gofmt -l cmd internal tools`; 출력이 비어 있어야 함 |
| 타입 검사·빌드 | `scripts/go build -o ./bin/ ./...` (exchange·loadgen·bench·api·domain·engine·strictjson·durable·wal·snapshot·runtime·probe, 출력은 Git 제외) |
| 기본 테스트 | `scripts/go test ./...` (CLI·bench·api·domain·engine·strictjson·wal·snapshot·runtime·probe) |
| race 검사 | `scripts/go test -race ./...` |
| cgo 미지원 경로 검사 | `CGO_ENABLED=0 scripts/go test ./...` |
| 실제 저장 호출 확인 | `scripts/go run ./tools/storageprobe -dir .` |
| WAL reader fuzz | `scripts/go test ./internal/wal -run '^$' -fuzz '^FuzzWALReader$' -fuzztime=30s -parallel=2` |
| WAL 실제 파일·프로세스 잠금 | `scripts/go test ./internal/wal -run '^TestNative' -v` (지원 플랫폼에서 실제 테스트 실행 확인) |
| 엔진 fuzz | `scripts/go test ./internal/engine -run '^$' -fuzz '^FuzzEngineInvariants$' -fuzztime=30s -parallel=2` |
| 엔진 단독 benchmark | `scripts/go test ./internal/engine -run '^$' -bench '^BenchmarkEngine$' -benchmem -benchtime=200ms -count=3` |
| runtime 통합 흐름 | `scripts/go test ./internal/runtime` (저장 경계 제어·동시 명령/조회·복구·종료) |
| 실제 프로세스 강제 종료/복구 | `scripts/go test ./internal/runtime -run '^TestNativeDurableReplayAfterProcessKill$' -v` (macOS/cgo, 전원 장애 실험 아님) |
| runtime snapshot 생성·종료·프로세스 복구 | `scripts/go test ./internal/runtime -run 'TestSnapshot|TestQueuedSnapshot|TestShutdownWaitsForBoth|TestWALFailureJoins|TestNativeSnapshot' -v` (native 사례는 macOS/cgo) |
| HTTP 입력·전송·거래/복구 | `scripts/go test ./internal/api -v` (loopback 권한 필요, APFS 통합 사례는 macOS/cgo) |
| CLI 실제 프로세스·signal·재시작 | `scripts/go test ./cmd/exchange -run '^TestNativeCLI' -v` (macOS/cgo·loopback, 개발 컴퓨터를 종료하지 않음) |
| HTTP 장애·과부하·프로세스 복구 | `scripts/go test ./internal/runtime -run '^TestNativeHTTP' -v -timeout=120s` (macOS/cgo·loopback, 테스트 소유 자식 프로세스만 kill) |
| 부하 생성 핵심·집계 | `scripts/go test -race ./internal/bench -timeout=60s` (입력·스케줄·표본/집계·loopback 전송, 기준 성능 측정 아님) |
| 추적된 변경의 공백 검사 | `git diff --check`; staged 변경은 `git diff --cached --check` |
| 새 문서의 공백 검사 | `git diff --no-index --check -- /dev/null <새 문서 경로>`; 각 파일에 실행하며 `<새 문서 경로>`는 실제 경로로 교체 |

`git diff --check`만으로 untracked 새 파일은 검사되지 않는다. `--no-index --check`는 정상 새 파일에도 차이가 있으면 종료 코드 1을 반환할 수 있으므로 공백 오류 진단과 종료 코드를 함께 확인한다. 문서 검증에는 상대 링크·제목 앵커·경로 대소문자·설명과 실제 설정의 일치 여부를 포함한다. 프로젝트 설명에는 미치환 입력 항목을 남기지 않고, 복사해서 쓸 `template.md`의 작업별 입력 안내는 유지한다. 현재 프로젝트 전용 자동 문서 검사 명령은 미구성이다.

## CLI 초기화·실행·종료

`scripts/go build -o ./bin/ ./...`로 `bin/exchange`를 만든다. [README의 실행 예시](../README.md#로컬-실행)는 기존 parent 아래 새 실험을 만들고 이어서 서버를 시작한다. 전체 flag는 `bin/exchange init -h`와 `bin/exchange serve -h`로 확인한다.

- `init --config ... --data-dir ...`: 4 MiB 이하 일반 JSON 파일을 엄격하게 검증한다. 일반 파일을 가리키는 symlink는 허용하지만 디렉터리·pipe·장치는 거절한다. 빈 디렉터리도 기존 대상이면 재사용하지 않으며 parent를 자동 생성하지 않는다. 성공 시 저장소를 닫은 뒤 `event=initialized`, runId, 절대 dataDir, configHash, 계정 수, maxCommands를 stdout의 JSON 한 줄로 출력한다. 저장 또는 출력 실패 뒤에도 생성된 데이터는 보존한다.
- `serve --data-dir ...`: 기존 Genesis/WAL/snapshot을 복구한다. 새 잔고나 Config를 받지 않는다. 모든 옵션을 검사하고 loopback listener를 확보한 후 복구하며, 검증·복구 동기화가 끝나야 HTTP 처리를 시작한다. stdout의 `event=ready` 한 줄에 runId·configHash·바이너리 build 정보, 절대 dataDir, 실제 listen 주소, runtime/HTTP 설정, 선택 snapshot·재생 명령 수·건너뛴 후보의 사유를 기록한다. port 0을 쓰면 이 주소로 접속한다.
- Ctrl+C/SIGINT·SIGTERM은 새 접수를 닫고 runtime의 접수된 명령과 HTTP 응답을 함께 기다린다. HTTP 종료 제한을 넘으면 연결을 닫고 실패로 종료하지만 저장 작업은 계속 기다린다. WAL·snapshot worker가 끝나기 전에 잠금을 해제하지 않는다. 저장 오류·예기치 않은 HTTP 종료·기동 보고 출력 실패에도 정리를 수행한다. 정상 종료 코드는 0, 오류는 stderr와 종료 코드 1로 보고한다.

| serve 옵션 | 기본값·의미 |
| --- | --- |
| `--listen` | `127.0.0.1:8080`; 숫자 loopback IPv4/IPv6만 허용, port 0 가능 |
| `--command-queue`, `--query-queue` | 각각 8,192 / 1,024 |
| `--request-limit`, `--pending-waiters` | 16,384 / 16; request-limit는 HTTP와 runtime 각각에 같은 한도를 설정 |
| `--batch-commands`, `--batch-bytes`, `--batch-delay` | 256 / 1,048,576 / 1ms |
| `--read-header-timeout`, `--read-body-timeout` | 각각 2s |
| `--result-timeout`, `--write-timeout` | 각각 5s; 접수 후 결과 timeout이 명령을 취소하지 않음 |
| `--http-shutdown-timeout` | 15s; HTTP 연결 대기 한도, 저장 drain의 제한 시간이 아님 |

시간 flag는 `1ms`, `5s`와 같은 Go duration 형식이다. 출력은 정규화된 문자열이며 `1h`는 `1h0m0s`가 된다. 잘못된 옵션·사용 중인 포트는 데이터 복구 전에 실패하므로 WAL tail을 변경하지 않는다. CLI에 자동 snapshot 주기는 없으며 현재는 Go `Runtime.Snapshot` API를 제공한다. HTTP 프로토콜·필드·계정 문맥은 [API 계약](design-docs/api-v1.md)이 소유한다.

## HTTP 부하 실행

아래는 짧은 기능 확인용 설정이며 기준 성능 목표가 아니다. 서버와 생성기는 같은 개발 컴퓨터에서 실행한다. 출력/거래 디렉터리는 매 실험 새로 만들고 다른 쓰기 클라이언트를 연결하지 않는다.

```sh
scripts/go build -o ./bin/ ./...
mkdir -p var/runs var/measurements
bin/exchange init --config config/examples/benchmark.json --data-dir var/runs/http-smoke
bin/exchange serve --data-dir var/runs/http-smoke --listen 127.0.0.1:8080 > var/measurements/http-smoke-server.json
```

서버 ready JSON이 기록된 뒤 다른 터미널에서 실행한다.

```sh
bin/loadgen --config config/examples/benchmark.json --workload config/examples/workload.json --server-report var/measurements/http-smoke-server.json --output var/measurements/http-smoke
```

끝나면 서버 터미널에서 Ctrl+C로 정상 종료한다. 이 예시 이름이 이미 존재하면 새로운 이름으로 바꾼다. 같은 run을 다시 측정하려 하면 asOfSeq=0 사전 검사가 거절한다. loadgen은 서버를 초기화·종료하지 않는다. `benchmark.json`의 등록 계정/초기 잔고와 `workload.json`의 요청률·seed·워밍업/측정 시간·비율·worker/deadline을 함께 기록한다. 시간 JSON 필드는 나노초, 금액/수량은 정수 문자열이다.

서버 ready 보고서는 build 정보와 configHash를 포함한 현재 버전의 파일이어야 한다. CLI는 설정 hash와 공개 API 초기 잔고를 대조하고 준비 주문 후 측정을 시작한다. 출력 파일·완료/실패 판정·지표 의미는 [측정 계약](design-docs/benchmark-v1.md#http-전송과-실행-파일)을 따른다. `completed.json`은 파일/측정 유효성 표식이며 성능 목표 통과가 아니다. SIGINT/SIGTERM 중단은 표본을 저장하고 실패로 종료한다. 출력 위치와 서버 데이터는 실패 후에도 보존한다.

## 코드 기준

Go 코드는 gofmt 형식을 사용하고 공개 함수·타입에 Go 문서 주석을 작성한다. 명령 재생의 결정성과 업무 상태의 단일 소유권은 [상세 설계](design-docs/exchange-v1.md)를 따른다.

다음 기존 사용자 규칙도 해당 언어로 코드를 추가할 때 유지한다.

- 새 TypeScript 코드는 strict mode를 사용한다. TypeScript를 도입하면 실제 타입 설정에서도 이를 보장한다.
- JavaScript/TypeScript 문자열은 작은따옴표를 사용한다.
- Promise 체인을 직접 다루기보다 async/await를 우선한다.
- JavaScript/TypeScript의 내보내는 함수에는 JSDoc 주석을 작성한다.

언어별 규칙은 formatter, linter, 타입 설정으로 가능한 범위에서 검증한다. 테스트에서 만든 전역 상태, mock, 타이머와 외부 자원은 종료 시 정리한다. source 변경 뒤 테스트 실행 의무와 의존성 추가 승인 기준은 [작업 절차](workflow.md)에서 관리한다.

## 테스트 환경

[도메인 테스트](../internal/domain/domain_test.go)는 정수 경계·JSON 정수 문자열·ID·초기 공급량을, [엔진 사례](../internal/engine/engine_test.go)는 거래 규칙·잔고·취소·중복·조회 사본을 검증한다. [독립 모델](../internal/engine/reference_test.go)은 총소유량에서 활성 주문 예약분을 다시 계산하고 후보 주문 전체를 정렬한다. 가격별 FIFO와 예약분 증감을 쓰는 실제 엔진과 다른 계산 경로로 결과·잔고·호가·체결·최초 결과를 매 명령 비교한다. 고정 시드 100개×128시도와 bounded fuzz, 동일 입력을 새 엔진에 다시 적용하는 결정성 검사를 포함한다.

[storageprobe 테스트](../tools/storageprobe/main_test.go)는 실제 파일 sync·부모 파일 보존·닫힌 FD 오류·프로세스 간 잠금과 해제를 검증한다. [지원 불가 테스트](../tools/storageprobe/unsupported_test.go)는 cgo를 끄면 성공으로 보고하지 않고 자기 임시 파일을 정리하는지 검사한다. P2의 [WAL 테스트](../internal/wal/codec_test.go)는 독립 golden bytes·모든 바이트 절단·모든 단일 비트 손상·정규화·순번/키 유일성을 확인한다. [I/O 오류 주입](../internal/wal/store_test.go)은 초기화·복구의 각 파일/디렉터리 호출, 실패 후 중단과 불변 최초 결과 재생을 검증한다. [실제 APFS 테스트](../internal/wal/store_darwin_test.go)는 strict sync·두 프로세스 잠금·같은 lock inode 유지·잘린 꼬리 복구를 확인한다. HTTP 검증은 아래 P5에 기록하며 실제 전원 장애는 별도 미완료 항목이다. 테스트 수집이 없는 패키지를 통과 실적으로 세지 않는다.

불변식·독립 기준 모델·재생 비교는 엔진 검증, 실제 파일·프로세스 종료는 저장/복구 검증, OS·전원 장애는 별도 환경 검증으로 구분한다. 데이터 디렉터리와 runId는 실험별로 격리한다. race/fuzz/profiler가 켜진 수치를 일반 성능 결과에 섞지 않는다.

[runtime 테스트](../internal/runtime/runtime_test.go)는 채널로 write/sync 경계를 멈춰 조기 적용·응답, pending 중복·충돌·한도, 큐/전역 포화, 취소·체결 경쟁, timeout 후 처리, 실패 후 중단과 종료 drain을 검사한다. [query 테스트](../internal/runtime/query_test.go)는 계정 범위·사본·체결 페이지 시점을, [실제 자식 프로세스 테스트](../internal/runtime/native_test.go)는 성공 응답 후와 저장 완료·적용 전 kill의 재생을 검증한다. 테스트 hook은 패키지 내부에만 두며 일반 호출에 장애 주입 옵션을 노출하지 않는다.

테스트 데이터와 외부 연동을 격리한다. 파괴적인 fixture나 migration 검증에는 폐기 가능한 전용 환경을 사용한다. 테스트 수집 실패, 전부 skip, 예상보다 줄어든 실행 범위를 통과로 해석하지 않는다.

P4 snapshot은 상태 직접 복원, 독립 golden bytes와 스트리밍 코덱, 파일 게시 오류를 검사한다. [실제 게시 테스트](../internal/snapshot/publish_darwin_test.go)는 APFS에서 별도 자식 프로세스만 7개 저장 경계에서 강제 종료하고 이전 파일 보존·재게시를 확인한다. 부모 개발 프로세스나 OS를 종료하지 않는다. 이 파일 계층 시험과 아래 runtime 통합 시험을 구분하고 실제 전원 장애로 보고하지 않는다. 새 코덱 fuzz는 `scripts/go test ./internal/snapshot -run '^$' -fuzz '^FuzzSnapshotReader$' -fuzztime=30s -parallel=2`다.

[runtime 복구 통합 테스트](../internal/runtime/recovery_native_test.go)는 실제 APFS의 최신·이전·Genesis 복구, 손상/입력 불일치 fallback, 기존 WAL 손상·잘못된 실험/버전 거절과 복구 후 재시도를 별도로 확인한다. [실행 중 생성 테스트](../internal/runtime/snapshot_test.go)는 한 사본 제한·timeout·저장 중 주문/조회·종료/실패 합류와 동시 입력의 사본별 재생 일치를 검사한다. [실제 runtime 게시 테스트](../internal/runtime/snapshot_native_test.go)는 저장 중 종료의 잠금 유지와 사본 생성 전·후 및 파일 게시 후 owner 확인 전 프로세스 kill을 검증한다. 사본 내부의 특정 명령어 위치에서 kill했다고 주장하지 않는다. 해당 검증은 전체 test/race 실행에 포함된다. HTTP 연결은 아래 P5 검증으로 이어지며 실제 전원 장애는 별도 미완료 항목이다.

P5의 [HTTP handler 검사](../internal/api/server_test.go)는 엄격한 JSON·헤더/계정 문맥·크기·오류/조회 DTO와 전송 슬롯을 검사한다. [실제 TCP 검사](../internal/api/network_test.go)는 헤더 8,192/8,193-byte 경계, 본문 4,096/4,097-byte 경계, 읽기/쓰기 timeout, 연결 단절과 포화를 확인한다. 읽지 않는 클라이언트의 송수신 버퍼를 줄여 실제 write timeout도 관찰한다. [APFS 통합](../internal/api/native_test.go)은 HTTP→runtime→WAL의 거래·잔고·중복·계정 범위·체결 페이지·동시 취소/체결·snapshot 재시작을 검증한다. timeout/연결 종료 뒤에도 이미 접수한 명령이 처리되고 최초 결과를 재조회할 수 있다.

HTTP 테스트는 127.0.0.1의 임의 포트를 열고 종료 시 서버·클라이언트·runtime을 정리한다. 샌드박스에서 bind가 거절되면 loopback 소켓을 허용하는 실행 권한으로 검사한다. 테스트를 skip하거나 recorder만 통과한 결과로 실제 네트워크 검증을 대신하지 않는다. HTTP부터 큐 포화·저장 오류·프로세스 종료까지의 통합 행렬은 아래 검사로 연결했다. 실제 전원 장애는 P5의 필수 미완료 항목이다.

[CLI 수명주기 검사](../cmd/exchange/lifecycle_test.go)는 HTTP/runtime의 완료 순서·HTTP 종료 timeout·runtime 오류·출력 실패 시 정리를 검사한다. [실제 자식 프로세스 검사](../cmd/exchange/native_test.go)는 테스트 실행 파일의 전용 TestMain 경로로 실제 main을 호출해 init·SIGINT/SIGTERM·접수 명령 drain·같은 포트/데이터 재시작·성공 응답 후 kill·잠금 경합·손상 기동 거절을 확인한다. 전용 진입 환경변수는 테스트 파일에만 있으며 일반 바이너리에 없다. 별도로 빌드한 `bin/exchange`에서도 초기화→HTTP 주문→SIGTERM→재시작→동일 응답/잔고를 확인했다. 두 경우 모두 자식 프로세스 시험이며 전원 장애 증거가 아니다.

P5의 [HTTP 장애 통합](../internal/runtime/http_fault_native_test.go)은 실제 loopback과 runtime을 사용한다. 명령/조회 큐·runtime 요청 한도, 적용 전후 panic, snapshot 저장 오류와 readiness 해제를 검사한다. WAL 실패 요청에 한해 테스트 adapter가 쓰기 전·부분 기록·완전한 기록/동기화 전·실제 strict sync 후 오류의 파일 상태를 만든다. 정상 실행에서 따로 얻은 동일 프레임 bytes를 사용하며 저장 실패 호출을 실제 Store.Append로 실행했다고 주장하지 않는다. 실제 WAL write/sync 함수의 오류 주입은 앞의 wal/store_test.go가 담당한다. 두 검증의 경계를 함께 기록한다.

[프로세스 통합](../internal/runtime/http_process_native_test.go)은 저장 결과 4종·적용 전후·응답 전/실제 본문 일부 전송 후·snapshot 사본 생성 전후/게시 전후의 12개 지점에서 자식만 종료한다. 재시작 시 기존 snapshot+WAL에서 허용한 순번을 확인하고, 재시도한 최초 응답·주문·잔고·호가·체결과 최종 WAL bytes를 정상 실행과 비교한다. snapshot 파일의 개별 write/sync/rename 호출은 P4의 파일 계층 7개 지점 검사와 구분한다. 사본 복사 내부나 매칭 내부의 특정 CPU 명령에서 kill한 것으로 표현하지 않는다.

[고정 seed 동시 입력](../internal/runtime/http_concurrent_native_test.go)은 4개 생산자의 고유 명령 64개를 각각 두 번 보내고 64개 잔고 조회를 수행한다. 응답 순번으로 정렬한 원본 입력을 별도 순차 엔진에 적용해 최초 결과·각 asOfSeq 잔고·최종 checkpoint를 비교한다. 이는 전송·순서·재생의 검사이며 독립 매칭 모델은 P1 테스트에 있다. [명시적 거래 사례](../internal/runtime/http_rules_native_test.go)는 가격/FIFO·여러 가격 체결 후 자기 거래 차단과 동시 잔고 소진의 기대 정수 값을 별도로 확인한다. 테스트 bridge와 환경변수는 `_test.go`에만 있으며 실행 바이너리에 장애 설정을 추가하지 않는다.

P6의 [bench](../internal/bench/plan.go)는 고정 입력·open-loop 스케줄·HTTP 전송·모든 예정 시도의 표본·고유 결과/분위수 집계를 제공한다. [측정 계약](design-docs/benchmark-v1.md)을 따른다. 가짜 executor의 알고리즘 검사와 실제 loopback 전송 검사를 분리한다. [loadgen 통합](../cmd/loadgen/native_test.go)은 실제 APFS/HTTP 거래·원본 파일 재집계·중단 후 모든 표본 보존·설정 불일치와 기존 run 재사용 거절을 확인한다. 관련 명령은 `scripts/go test -race ./internal/bench ./cmd/loadgen -timeout=120s`다. 서버 계측·기준 지속 처리량 측정은 남아 있다.

## CI와 릴리스

origin은 [gaon000/exchange](https://github.com/gaon000/exchange)이다. 커밋·병합·push 상태는 Git 기록으로 확인한다. CI workflow·릴리스·배포 설정은 없다. 호스팅 대상과 원격 보호 규칙은 미확정이다. 도입 후 실제 workflow 경로, 자동 검사, 확인한 보호 규칙과 배포 후 확인 방법을 기록한다.

P1 엔진 테스트와 [단독 benchmark](benchmarks/p1-memory-engine.md)는 API·WAL·p99 전체 경로 검증을 의미하지 않는다. P0~P5의 실제 결과는 [구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)에 기록한다.
