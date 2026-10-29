# exchange

학습·포트폴리오용 가상자산 현물 모의 거래소다. Go 단일 프로세스에서 단일 거래쌍의 지정가 주문·부분 체결·취소를 처리하고, p99 응답 시간 상한을 지키는 최대 지속 처리량을 측정한다.

2026-10-29 기준 P4까지 도메인·엔진·Genesis/WAL·단일 소유자 runtime과 스냅샷 생성·재시작 복구를 구현했다. P5의 HTTP 계층과 초기화·실행 CLI를 연결해 실제 loopback에서 거래·중복·timeout·과부하·저장 오류·종료·재시작을 검증했다. 최종 결과물은 거래 API, 부하 생성기와 성능 보고서이며 실거래·실행 중 입출금·수수료·웹 화면은 첫 버전에서 제외한다.

- [제품 명세](docs/product-specs/exchange.md): 거래 규칙과 성능 목표.
- [상세 설계](docs/design-docs/exchange-v1.md): 주문 상태, 잔고 예약, 로그 재생과 중복 처리.
- [API 계약](docs/design-docs/api-v1.md)과 [저장 형식](docs/design-docs/storage-v1.md): P0에서 확정한 정수 단위·요청·응답·WAL·플랫폼 규칙.
- [단계별 구현 계획](docs/exec-plans/active/20261029-1531-exchange-v1.md): 준비부터 장애 검증·성능 실험까지의 산출물과 통과 조건.
- [작업 안내](AGENTS.md): 개발 작업의 진입점과 공통 원칙.
- [문서 지도](docs/index.md): 문서별 책임과 적용 이력.
- [전체 구조](ARCHITECTURE.md): 구현한 모듈과 예정 모듈의 책임.
- [개발 기준](docs/development.md): 도구·명령의 구성 상태와 개발 규칙.

개발 환경은 `sh scripts/setup-go.sh`로 준비하고 `scripts/go test ./...`로 CLI·bench·api·도메인·엔진·WAL·runtime·snapshot·strictjson·storageprobe 테스트를 실행한다. HTTP 테스트에는 loopback 소켓 권한이 필요하다. 지원 환경은 macOS arm64·로컬 APFS·cgo이며 전체 명령은 [개발 기준](docs/development.md)을 따른다. [P1 벤치마크](docs/benchmarks/p1-memory-engine.md)는 메모리 엔진 단독 수치다. [HTTP 부하 생성기](docs/design-docs/benchmark-v1.md)는 준비 검증·원본 기록까지 구현했다. 실행은 [HTTP 부하 실행](docs/development.md#http-부하-실행)을 따른다. 서버 구간·CPU/메모리/GC 계측은 `--metrics-file`로 기록한다. 스냅샷 주기·프로파일 수집은 선택 옵션이다. [실험 실행기](docs/development.md#묶음-실험-실행)는 새 서버/생성기와 환경·바이너리·원본 묶음을 관리한다. [P6 기준 측정](docs/benchmarks/p6-baseline.md)에서 정상 조건의 3회 통과와 스냅샷 조건의 목표 미달을 기록했다. [P7 비교](docs/benchmarks/p7-snapshot-sorting.md)에서 정렬 이동 후보를 검증했으나 반복 기준 미달로 채택하지 않고 P6 구현을 유지했다. [스냅샷 처리 한계](docs/benchmarks/p7-snapshot-capacity.md)는 고정된 20ms 기준에서 3,000 requests/s를 세 번 채택했다. 실제 전원 장애 검증은 미완료이며 CI도 미구성이다.

## 로컬 실행

저장소 루트에서 빌드하고 새 실험을 한 번 초기화한다. `demo`가 이미 있으면 다른 새 이름을 사용한다.

```sh
scripts/go build -o ./bin/ ./...
mkdir -p var/runs
bin/exchange init --config config/examples/local.json --data-dir var/runs/demo
bin/exchange serve --data-dir var/runs/demo --listen 127.0.0.1:8080
```

`init`은 새 runId와 초기 잔고를 저장하고 기존 디렉터리는 덮어쓰지 않는다. `serve`는 기존 상태를 복구하고 준비가 끝나면 runId·실제 주소·설정·복구 결과를 JSON 한 줄로 출력한다. 이 runId로 [API 요청](docs/design-docs/api-v1.md#4-변경-api)을 보낸다. 종료는 Ctrl+C 또는 SIGTERM이며 접수된 명령의 저장과 응답을 기다린다. 재시작할 때는 같은 `serve` 명령만 실행한다. 추가 옵션과 HTTP 종료 제한은 [CLI 사용법](docs/development.md#cli-초기화실행종료)을 따른다.
