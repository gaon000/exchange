# exchange

학습·포트폴리오용 가상자산 현물 모의 거래소다. Go 단일 프로세스에서 단일 거래쌍의 지정가 주문·부분 체결·취소를 처리하고, p99 응답 시간 상한을 지키는 최대 지속 처리량을 측정한다.

2026-11-12 기준 거래 API·정수 엔진·Genesis/WAL·단일 owner·snapshot을 구현하고, 실제 HTTP 거래·과부하/저장 오류·응답 유실·프로세스 종료/복구를 검증했다. 거래·복구 데모, 수동 거래 CLI, 부하/판정/진단 도구와 자료 보관 미리보기를 제공한다. **실제 OS·전원 장애 검증은 미완료·보류**이며 이를 필수로 포함하는 v1 전체 계획은 Blocked다. 실거래·실행 중 입출금·수수료·웹 화면·운영 배포는 첫 버전에서 제외한다.

- [프로젝트 결과 보고서](docs/project-results.md): 구현 구조·거래 규칙·검증된 복구 범위·시점별 성능 결과와 한계.

- [제품 명세](docs/product-specs/exchange.md): 거래 규칙과 성능 목표.
- [상세 설계](docs/design-docs/exchange-v1.md): 주문 상태, 잔고 예약, 로그 재생과 중복 처리.
- [API 계약](docs/design-docs/api-v1.md)과 [저장 형식](docs/design-docs/storage-v1.md): P0에서 확정한 정수 단위·요청·응답·WAL·플랫폼 규칙.
- [단계별 구현 계획](docs/exec-plans/active/20261029-1531-exchange-v1.md): 준비부터 장애 검증·성능 실험까지의 산출물과 통과 조건.
- [작업 안내](AGENTS.md): 개발 작업의 진입점과 공통 원칙.
- [문서 지도](docs/index.md): 문서별 책임과 적용 이력.
- [전체 구조](ARCHITECTURE.md): 구현한 모듈과 예정 모듈의 책임.
- [실험 결과 비교 보고서](docs/experiment-comparison.md): 기준·후보 원본 검증과 JSON·Markdown 비교표.
- [재현 가능한 실험 도구](docs/diagnostic-tools.md): 새 실행·전체 복구·원본 검증·trace/OS I/O·타이머 진단.
- [고정 seed 거래·복구 검사](docs/random-recovery.md): HTTP 거래·snapshot·재시작 조합과 실패 명세 재현.
- [실험 자료 패키징·복원](docs/evidence-packages.md): 이전 실행·고정 빌드 수집, 새 폴더 복원과 검증·비교표 재생성.
- [실험 자료 보관·정리 미리보기](docs/artifact-retention.md): var 용량·참조 관계·원본/고정 빌드 보호와 삭제 후보 목록.
- [개발 기준](docs/development.md): 도구·명령의 구성 상태와 개발 규칙.

개발 환경은 `sh scripts/setup-go.sh`로 준비한다. 지원 저장 환경은 macOS arm64·로컬 APFS·cgo이며 전체 명령은 [개발 기준](docs/development.md)을 따른다. Linux portable CI의 통과는 Linux 거래 저장소 지원을 뜻하지 않는다. [CI](.github/workflows/ci.yml)는 일반 로직과 macOS/APFS 검증을 분리한다.

성능 수치는 빌드·조건·시점별로 구분한다. [2026-11-09 누적 24회 판정](docs/benchmarks/normal-capacity-recheck.md)에서는 정상 조건 2,000 requests/s만 채택됐다. 과거 더 높은 채택점과 후속 실패도 보존하며 현재 master의 최대 처리량·서비스 SLA로 해석하지 않는다. snapshot 고정 상태의 생성/복구 비용 감소와 동시 주문 성능의 구분은 [결과 보고서](docs/project-results.md#5-성능-결과와-해석)를 따른다.

## 한 명령 거래 데모

macOS arm64/APFS 환경에서 저장소 루트에서 실행한다.

```sh
sh scripts/demo.sh
```

기본 all 모드에서 주문·부분 체결·취소, 자기 거래 차단·예약금/가격 개선 차액 반환·중복 ID 충돌과 정상 WAL·스냅샷·소유 서버 강제 종료 후 복구를 자동 검증한다. 매 재기동에서 POST 재시도 전에 상태와 최초 응답을 GET으로 확인한다. `--scenario normal|snapshot|crash`로 개별 경로도 선택할 수 있다. 매번 새 `var/demos/` 폴더에 실행 결과를 보존한다. 시나리오·기대 잔고·실패 시 확인할 파일은 [거래 데모 안내](docs/trading-demo.md)를 따른다.

## 로컬 실행

원하는 주문을 직접 넣으려면 [거래용 CLI](docs/trading-cli.md)를 사용한다. 계정·가격·수량을 지정한 주문/취소와 잔고·호가·체결·주문 조회, 저장한 요청으로 타임아웃 뒤 같은 ID 결과 조회/재시도를 제공한다.

저장소 루트에서 빌드하고 새 실험을 한 번 초기화한다. `demo`가 이미 있으면 다른 새 이름을 사용한다.

```sh
scripts/go build -o ./bin/ ./...
mkdir -p var/runs
bin/exchange init --config config/examples/local.json --data-dir var/runs/demo
bin/exchange serve --data-dir var/runs/demo --listen 127.0.0.1:8080
```

`init`은 새 runId와 초기 잔고를 저장하고 기존 디렉터리는 덮어쓰지 않는다. `serve`는 기존 상태를 복구하고 준비가 끝나면 runId·실제 주소·설정·복구 결과를 JSON 한 줄로 출력한다. 이 runId로 [API 요청](docs/design-docs/api-v1.md#4-변경-api)을 보낸다. 종료는 Ctrl+C 또는 SIGTERM이며 접수된 명령의 저장과 응답을 기다린다. 재시작할 때는 같은 `serve` 명령만 실행한다. 추가 옵션과 HTTP 종료 제한은 [CLI 사용법](docs/development.md#cli-초기화실행종료)을 따른다.
