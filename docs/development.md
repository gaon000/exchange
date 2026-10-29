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

현재 실행 가능한 것은 개발 probe이며 거래 서버가 아니다. probe는 `-dir`로 지정한 기존 parent에 고유 임시 디렉터리를 만들고 자기 파일만 정리한다. 실제 실험 데이터에 오류를 주입하는 도구가 아니다.

거래 서버의 환경변수 loader는 아직 없다. 초기 입력은 [local.json](../config/examples/local.json) fixture와 [API 계약](design-docs/api-v1.md)을 기준으로 P1/P2에서 연결한다. 현재 fixture에는 비밀값이 없다. 실제 credential이 필요해지면 안전한 별도 경로로 관리한다.

## 명령

명령은 저장소 루트 기준이다. 다른 작업 경로가 필요하면 해당 행에 명시한다. 실행 가능한 스크립트가 없는 항목을 예시 명령으로 대체하지 않는다.

| 작업 | 실제 명령·구성 상태 |
| --- | --- |
| 개발 toolchain 준비 | `sh scripts/setup-go.sh` (macOS arm64) |
| toolchain 확인 | `scripts/go version`; `scripts/go env GOVERSION GOOS GOARCH CGO_ENABLED CC` |
| Go 의존성 | go.mod만 존재, 외부 require/go.sum 없음. 추가 production dependency는 승인 전 설치하지 않음 |
| 로컬 실행 | 미구성: 애플리케이션과 실행 진입점 없음 |
| 정적 분석 | `scripts/go vet ./...` |
| 포맷 검사 | `.tools/go1.27.3/bin/gofmt -l tools`; 출력이 비어 있어야 함. 소스 경로가 늘면 대상도 추가 |
| 타입 검사·빌드 | `scripts/go build -o ./bin/ ./...` (현재 probe만 대상, 출력은 Git 제외) |
| 기본 테스트 | `scripts/go test ./...` (현재 probe만 대상) |
| race 검사 | `scripts/go test -race ./...` |
| cgo 미지원 경로 검사 | `CGO_ENABLED=0 scripts/go test ./...` |
| 실제 저장 호출 확인 | `scripts/go run ./tools/storageprobe -dir .` |
| 통합·주요 흐름 테스트 | 미구성: 애플리케이션·테스트 환경 없음 |
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

[storageprobe 테스트](../tools/storageprobe/main_test.go)는 실제 파일 sync·부모 파일 보존·닫힌 FD 오류·프로세스 간 잠금과 해제를 검증한다. [지원 불가 테스트](../tools/storageprobe/unsupported_test.go)는 cgo를 끄면 성공으로 보고하지 않고 자기 임시 파일을 정리하는지 검사한다. 거래 엔진·WAL·API·fuzz·benchmark·장애 주입은 P1 이후 구현 대상이다. 테스트 수집이 없는 패키지를 통과 실적으로 세지 않는다.

불변식·독립 기준 모델·재생 비교는 엔진 검증, 실제 파일·프로세스 종료는 저장/복구 검증, OS·전원 장애는 별도 환경 검증으로 구분한다. 데이터 디렉터리와 runId는 실험별로 격리한다. race/fuzz/profiler가 켜진 수치를 일반 성능 결과에 섞지 않는다.

테스트 데이터와 외부 연동을 격리한다. 파괴적인 fixture나 migration 검증에는 폐기 가능한 전용 환경을 사용한다. 테스트 수집 실패, 전부 skip, 예상보다 줄어든 실행 범위를 통과로 해석하지 않는다.

## CI와 릴리스

origin은 [gaon000/exchange](https://github.com/gaon000/exchange)이며 master에 설계 문서가 push되어 있다. CI workflow·릴리스·배포 설정은 없다. 호스팅 대상과 원격 보호 규칙은 미확정이다. 도입 후 실제 workflow 경로, 자동 검사, 확인한 보호 규칙과 배포 후 확인 방법을 기록한다.

개발 probe의 빌드·테스트는 거래 엔진·API·WAL 검증을 의미하지 않는다. P0의 실제 결과는 [구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)에 기록한다.
