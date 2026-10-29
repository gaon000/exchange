# exchange

학습·포트폴리오용 가상자산 현물 모의 거래소다. Go 단일 프로세스에서 단일 거래쌍의 지정가 주문·부분 체결·취소를 처리하고, p99 응답 시간 상한을 지키는 최대 지속 처리량을 측정한다.

2026-10-29 기준 P1의 정수 도메인과 결정적인 메모리 엔진을 구현했다. 가격·FIFO 매칭, 잔고 예약·해제, 자기 거래 차단과 최초 결과 재사용을 검증했다. 다음은 P2의 WAL·영구 저장 계층이다. 최종 결과물은 거래 API, 부하 생성기와 성능 보고서이며 실거래·실행 중 입출금·수수료·웹 화면은 첫 버전에서 제외한다.

- [제품 명세](docs/product-specs/exchange.md): 거래 규칙과 성능 목표.
- [상세 설계](docs/design-docs/exchange-v1.md): 주문 상태, 잔고 예약, 로그 재생과 중복 처리.
- [API 계약](docs/design-docs/api-v1.md)과 [저장 형식](docs/design-docs/storage-v1.md): P0에서 확정한 정수 단위·요청·응답·WAL·플랫폼 규칙.
- [단계별 구현 계획](docs/exec-plans/active/20261029-1531-exchange-v1.md): 준비부터 장애 검증·성능 실험까지의 산출물과 통과 조건.
- [작업 안내](AGENTS.md): 개발 작업의 진입점과 공통 원칙.
- [문서 지도](docs/index.md): 문서별 책임과 적용 이력.
- [전체 구조](ARCHITECTURE.md): 구현한 모듈과 예정 모듈의 책임.
- [개발 기준](docs/development.md): 도구·명령의 구성 상태와 개발 규칙.

개발 환경은 `sh scripts/setup-go.sh`로 준비하고 `scripts/go test ./...`로 도메인·엔진·storageprobe 테스트를 실행한다. 지원 환경과 전체 명령은 [개발 기준](docs/development.md)을 따른다. [P1 벤치마크](docs/benchmarks/p1-memory-engine.md)는 메모리 엔진 단독 수치다. WAL·거래 API·부하 생성기·CI는 아직 없으며 전체 거래 경로의 성능·복구는 미검증이다.
