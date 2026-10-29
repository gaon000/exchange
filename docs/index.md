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

## 작업별 읽기

제품 범위를 정할 때는 [exchange 제품 명세 초안](product-specs/exchange.md)에서 확인된 목표와 열린 질문을 먼저 확인한다. 작은 수정은 구조와 개발·작업 기준을 확인한 뒤 관련 코드를 읽는다. 큰 작업은 관련 실행 계획도 확인한다. 완료 계획은 과거 판단을 이해하는 자료로 읽고 현재 구현과 다시 대조한다.

## 선택 문서

Product Specs는 거래소라는 목표만 정해진 현재 단계에서 사용자·거래 대상·첫 버전 범위를 구분해 기록하기 위해 도입했다. 미확정 질문을 임의의 요구사항으로 채우지 않는다.

Design Docs와 Decisions는 구체적인 설계 검토나 기술 결정이 생길 때 추가한다. Glossary는 용어가 혼동될 때, skill은 반복 작업이 확인될 때 검토한다. 현재는 이 영역의 문서나 설치 자산을 추가하지 않았다. 실제로 추가한 문서만 이 지도에 링크하며, 한 문서가 너무 커지면 요약과 관련 상세 문서의 링크를 남긴다.

## 템플릿 적용 이력

| 원본 저장소·조회 위치 | 기준 ref | 확인한 commit·버전 | 적용일 | 템플릿 경로 → 대상 경로 | 변경 요약 |
| --- | --- | --- | --- | --- | --- |
| [gaon000/ai-dev-template](https://github.com/gaon000/ai-dev-template) | master | [67abe615afa2d3c056bbf39e545b32e19753facd](https://github.com/gaon000/ai-dev-template/commit/67abe615afa2d3c056bbf39e545b32e19753facd) | 2026-10-28 (Asia/Seoul) | `starter/AGENTS.md` → `AGENTS.md`; `starter/ARCHITECTURE.md` → `ARCHITECTURE.md`; `starter/docs/` → `docs/`; `optional/product-specs/` → `docs/product-specs/` | 최초 도입. 빈 저장소의 현재 상태, 거래소 구축 목표, 기존 사용자 개발 규칙을 반영. `README.md`와 `docs/product-specs/exchange.md`는 프로젝트 전용 문서로 추가. 템플릿의 패키지 설정·검사기·CI는 복사하지 않음. |

기존 사용자 수정과 이전 적용 이력을 보존하고 새 적용 내역을 추가한다. 재적용 시 이전 기준 버전과 현재 문서를 비교하되, 기준 버전이 없거나 접근할 수 없으면 그 한계를 기록한다. 이 기록은 문서 적용의 근거이며 애플리케이션 실행·테스트·배포 완료의 증거가 아니다.
