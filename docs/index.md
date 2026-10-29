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

v1의 범위와 거래 규칙은 [제품 명세](product-specs/exchange.md), 주문 상태·잔고·로그·중복 계약은 [상세 설계](design-docs/exchange-v1.md), 구체적인 입력·저장 형식은 [API](design-docs/api-v1.md)와 [Storage](design-docs/storage-v1.md)를 읽는다. [단계별 구현 계획](exec-plans/active/20261029-1531-exchange-v1.md)은 P4까지 완료했고 P5 HTTP 계층·초기화/실행 CLI·HTTP 장애 통합 검증을 연결했다. P6의 [HTTP 부하 생성·집계·원본 기록](design-docs/benchmark-v1.md)을 구현했다. 서버 구간·프로세스 자원 계측을 추가했다. 스냅샷/프로파일 옵션과 새 프로세스 실행·환경/바이너리/원본 묶음을 연결했다. 반복 기준 측정을 완료했고 P7 개선·최종 비교와 실제 전원 장애 검증은 남아 있다. [P1 벤치마크](benchmarks/p1-memory-engine.md)는 엔진 단독 측정이다. [P6 기준과 결과](benchmarks/p6-baseline.md)는 전체 경로 초기 탐색·고정 조건·14개 실행의 재검증과 정상 조건의 반복 baseline을 기록한다. [P7 정렬 이동](benchmarks/p7-snapshot-sorting.md)은 구현·회귀 검증을 마쳤고 반복 성능 비교는 남아 있다. 작은 수정은 관련 기준과 실제 경로를 확인한다. 완료 계획은 과거 판단을 이해하는 자료이며 현재 구현과 다시 대조한다.

## 선택 문서

Product Specs는 사용자 동작·범위를, Design Docs는 주문 상태·잔고·영구 저장·복구의 구체적인 계약을 소유한다. 이번에 합의된 설계를 문서화하면서 Design Docs를 추가했다.

작업의 결정 이유는 설계의 대안 비교와 실행 계획의 Decision Log에 남긴다. 별도 Decisions·Glossary·skill은 아직 도입하지 않았다. 필요한 경우에만 추가하고 실제 존재하는 문서를 지도에 연결한다.

## 템플릿 적용 이력

| 원본 저장소·조회 위치 | 기준 ref | 확인한 commit·버전 | 적용일 | 템플릿 경로 → 대상 경로 | 변경 요약 |
| --- | --- | --- | --- | --- | --- |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-28 (Asia/Seoul) | `starter/AGENTS.md` → `AGENTS.md`; `starter/ARCHITECTURE.md` → `ARCHITECTURE.md`; `starter/docs/` → `docs/`; `optional/product-specs/` → `docs/product-specs/` | 최초 도입. 빈 저장소의 현재 상태, 거래소 구축 목표, 기존 사용자 개발 규칙을 반영. `README.md`와 `docs/product-specs/exchange.md`는 프로젝트 전용 문서로 추가. 템플릿의 패키지 설정·검사기·CI는 복사하지 않음. |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-29 (Asia/Seoul) | `optional/design-docs/index.md`, `optional/design-docs/template.md` → `docs/design-docs/index.md`, `docs/design-docs/template.md` | 선택 설계 영역 추가. index를 프로젝트에 맞게 조정. `exchange-v1.md`와 active 구현 계획은 합의한 내용을 별도 작성. 전체 템플릿 재적용은 아님. |

기존 사용자 수정과 이전 적용 이력을 보존하고 새 적용 내역을 추가한다. 재적용 시 이전 기준 버전과 현재 문서를 비교하되, 기준 버전이 없거나 접근할 수 없으면 그 한계를 기록한다. 이 기록은 문서 적용의 근거이며 애플리케이션 실행·테스트·배포 완료의 증거가 아니다.
