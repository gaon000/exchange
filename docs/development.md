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

현재 실행 진입점은 개발 probe다. Go runtime API와 HTTP 서버 라이브러리를 제공하지만 서버 CLI는 아직 없다. HTTP 계층은 테스트에서 실제 loopback listener와 runtime을 조립해 실행한다. probe는 `-dir`로 지정한 기존 parent에 고유 임시 디렉터리를 만들고 자기 파일만 정리한다. 실제 실험 데이터에 오류를 주입하는 도구가 아니다.

거래 서버의 환경변수 loader는 아직 없다. P1은 [local.json](../config/examples/local.json) fixture를 읽는 도메인 테스트와 명시적인 Config를 받는 엔진을 제공한다. P2의 wal.DecodeConfig와 wal.Init/Open은 설정 파싱과 저장소 수명주기를 제공한다. 실제 서버 CLI loader는 P5에서 연결한다. fixture에는 비밀값이 없다. 실제 credential이 필요해지면 안전한 별도 경로로 관리한다.

## 명령

명령은 저장소 루트 기준이다. 다른 작업 경로가 필요하면 해당 행에 명시한다. 실행 가능한 스크립트가 없는 항목을 예시 명령으로 대체하지 않는다.

| 작업 | 실제 명령·구성 상태 |
| --- | --- |
| 개발 toolchain 준비 | `sh scripts/setup-go.sh` (macOS arm64) |
| toolchain 확인 | `scripts/go version`; `scripts/go env GOVERSION GOOS GOARCH CGO_ENABLED CC` |
| Go 의존성 | go.mod만 존재, 외부 require/go.sum 없음. 추가 production dependency는 승인 전 설치하지 않음 |
| 로컬 실행 | HTTP 서버 CLI 미구성; runtime.Open/Submit/Read/Snapshot/Shutdown Go API 제공 |
| 정적 분석 | `scripts/go vet ./...` |
| 포맷 검사 | `.tools/go1.27.3/bin/gofmt -l internal tools`; 출력이 비어 있어야 함 |
| 타입 검사·빌드 | `scripts/go build -o ./bin/ ./...` (api·domain·engine·strictjson·durable·wal·snapshot·runtime·probe, 출력은 Git 제외) |
| 기본 테스트 | `scripts/go test ./...` (api·domain·engine·strictjson·wal·snapshot·runtime·probe) |
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
| 추적된 변경의 공백 검사 | `git diff --check`; staged 변경은 `git diff --cached --check` |
| 새 문서의 공백 검사 | `git diff --no-index --check -- /dev/null <새 문서 경로>`; 각 파일에 실행하며 `<새 문서 경로>`는 실제 경로로 교체 |

`git diff --check`만으로 untracked 새 파일은 검사되지 않는다. `--no-index --check`는 정상 새 파일에도 차이가 있으면 종료 코드 1을 반환할 수 있으므로 공백 오류 진단과 종료 코드를 함께 확인한다. 문서 검증에는 상대 링크·제목 앵커·경로 대소문자·설명과 실제 설정의 일치 여부를 포함한다. 프로젝트 설명에는 미치환 입력 항목을 남기지 않고, 복사해서 쓸 `template.md`의 작업별 입력 안내는 유지한다. 현재 프로젝트 전용 자동 문서 검사 명령은 미구성이다.

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

[storageprobe 테스트](../tools/storageprobe/main_test.go)는 실제 파일 sync·부모 파일 보존·닫힌 FD 오류·프로세스 간 잠금과 해제를 검증한다. [지원 불가 테스트](../tools/storageprobe/unsupported_test.go)는 cgo를 끄면 성공으로 보고하지 않고 자기 임시 파일을 정리하는지 검사한다. P2의 [WAL 테스트](../internal/wal/codec_test.go)는 독립 golden bytes·모든 바이트 절단·모든 단일 비트 손상·정규화·순번/키 유일성을 확인한다. [I/O 오류 주입](../internal/wal/store_test.go)은 초기화·복구의 각 파일/디렉터리 호출, 실패 후 중단과 불변 최초 결과 재생을 검증한다. [실제 APFS 테스트](../internal/wal/store_darwin_test.go)는 strict sync·두 프로세스 잠금·같은 lock inode 유지·잘린 꼬리 복구를 확인한다. API·실제 전원 장애 검증은 후속 단계다. 테스트 수집이 없는 패키지를 통과 실적으로 세지 않는다.

불변식·독립 기준 모델·재생 비교는 엔진 검증, 실제 파일·프로세스 종료는 저장/복구 검증, OS·전원 장애는 별도 환경 검증으로 구분한다. 데이터 디렉터리와 runId는 실험별로 격리한다. race/fuzz/profiler가 켜진 수치를 일반 성능 결과에 섞지 않는다.

[runtime 테스트](../internal/runtime/runtime_test.go)는 채널로 write/sync 경계를 멈춰 조기 적용·응답, pending 중복·충돌·한도, 큐/전역 포화, 취소·체결 경쟁, timeout 후 처리, 실패 후 중단과 종료 drain을 검사한다. [query 테스트](../internal/runtime/query_test.go)는 계정 범위·사본·체결 페이지 시점을, [실제 자식 프로세스 테스트](../internal/runtime/native_test.go)는 성공 응답 후와 저장 완료·적용 전 kill의 재생을 검증한다. 테스트 hook은 패키지 내부에만 두며 일반 호출에 장애 주입 옵션을 노출하지 않는다.

테스트 데이터와 외부 연동을 격리한다. 파괴적인 fixture나 migration 검증에는 폐기 가능한 전용 환경을 사용한다. 테스트 수집 실패, 전부 skip, 예상보다 줄어든 실행 범위를 통과로 해석하지 않는다.

P4 snapshot은 상태 직접 복원, 독립 golden bytes와 스트리밍 코덱, 파일 게시 오류를 검사한다. [실제 게시 테스트](../internal/snapshot/publish_darwin_test.go)는 APFS에서 별도 자식 프로세스만 7개 저장 경계에서 강제 종료하고 이전 파일 보존·재게시를 확인한다. 부모 개발 프로세스나 OS를 종료하지 않는다. 이 파일 계층 시험과 아래 runtime 통합 시험을 구분하고 실제 전원 장애로 보고하지 않는다. 새 코덱 fuzz는 `scripts/go test ./internal/snapshot -run '^$' -fuzz '^FuzzSnapshotReader$' -fuzztime=30s -parallel=2`다.

[runtime 복구 통합 테스트](../internal/runtime/recovery_native_test.go)는 실제 APFS의 최신·이전·Genesis 복구, 손상/입력 불일치 fallback, 기존 WAL 손상·잘못된 실험/버전 거절과 복구 후 재시도를 별도로 확인한다. [실행 중 생성 테스트](../internal/runtime/snapshot_test.go)는 한 사본 제한·timeout·저장 중 주문/조회·종료/실패 합류와 동시 입력의 사본별 재생 일치를 검사한다. [실제 runtime 게시 테스트](../internal/runtime/snapshot_native_test.go)는 저장 중 종료의 잠금 유지와 사본 생성 전·후 및 파일 게시 후 owner 확인 전 프로세스 kill을 검증한다. 사본 내부의 특정 명령어 위치에서 kill했다고 주장하지 않는다. 해당 검증은 전체 test/race 실행에 포함된다. HTTP 연결은 아래 P5 검증으로 이어지며 실제 전원 장애는 별도 미완료 항목이다.

P5의 [HTTP handler 검사](../internal/api/server_test.go)는 엄격한 JSON·헤더/계정 문맥·크기·오류/조회 DTO와 전송 슬롯을 검사한다. [실제 TCP 검사](../internal/api/network_test.go)는 헤더 8,192/8,193-byte 경계, 본문 4,096/4,097-byte 경계, 읽기/쓰기 timeout, 연결 단절과 포화를 확인한다. 읽지 않는 클라이언트의 송수신 버퍼를 줄여 실제 write timeout도 관찰한다. [APFS 통합](../internal/api/native_test.go)은 HTTP→runtime→WAL의 거래·잔고·중복·계정 범위·체결 페이지·동시 취소/체결·snapshot 재시작을 검증한다. timeout/연결 종료 뒤에도 이미 접수한 명령이 처리되고 최초 결과를 재조회할 수 있다.

HTTP 테스트는 127.0.0.1의 임의 포트를 열고 종료 시 서버·클라이언트·runtime을 정리한다. 샌드박스에서 bind가 거절되면 loopback 소켓을 허용하는 실행 권한으로 검사한다. 테스트를 skip하거나 recorder만 통과한 결과로 실제 네트워크 검증을 대신하지 않는다. 서버 CLI·HTTP부터 큐 포화/저장 오류/프로세스 종료까지의 추가 통합 행렬·실제 전원 장애는 P5의 남은 작업이다.

## CI와 릴리스

origin은 [gaon000/exchange](https://github.com/gaon000/exchange)이다. 커밋·병합·push 상태는 Git 기록으로 확인한다. CI workflow·릴리스·배포 설정은 없다. 호스팅 대상과 원격 보호 규칙은 미확정이다. 도입 후 실제 workflow 경로, 자동 검사, 확인한 보호 규칙과 배포 후 확인 방법을 기록한다.

P1 엔진 테스트와 [단독 benchmark](benchmarks/p1-memory-engine.md)는 API·WAL·p99 전체 경로 검증을 의미하지 않는다. P0~P4의 실제 결과는 [구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)에 기록한다.
