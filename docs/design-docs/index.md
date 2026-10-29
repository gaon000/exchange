# Design Docs

기능의 구조·계약·대안·실패 경계를 기록한다. 제품 동작은 [제품 명세](../product-specs/index.md), 진행과 실제 검증은 [실행 계획](../exec-plans/index.md)이 소유한다.

## 사용

[템플릿](template.md)을 기능 이름의 문서로 복사한다. 상태는 Draft, Accepted, Superseded를 사용한다. Accepted는 합의된 설계이며 구현·배포 완료를 의미하지 않는다. 합의한 계약을 바꿀 때는 이유와 관련 계획을 함께 갱신한다. 대체 문서를 만들면 이전 문서를 Superseded로 바꾸고 양쪽에 연결한다.

## 검토 기준

상태 변경 진입점, 명령 순서, 데이터 소유권, 중복·timeout, 현재 상태와 입력 snapshot, 실패 시 복구, 호환성과 관찰 가능한 검증을 확인한다. 실제 위험에 맞는 항목을 사용하며 기능에 필요 없는 점검을 강제하지 않는다.

## 목록

| 문서 | 상태 | 범위 |
| --- | --- | --- |
| [exchange v1](exchange-v1.md) | Accepted | 주문 상태, 잔고 예약·해제, 명령 로그·스냅샷·중복·장애 복구 |
| [API v1](api-v1.md) | Accepted | P0 정수 단위·한도·정규화·로컬 HTTP·최초 응답·조회 계약 |
| [Storage v1](storage-v1.md) | Accepted | P0 파일 형식·버전·체크섬·strict sync·잠금·복구 판정 |
| [부하 생성과 측정 v1](benchmark-v1.md) | Accepted | P6 입력·예정 발사·HTTP 전송/준비·원본 기록·누락/중복 집계 계약. 서버 구간/자원 계측 포함. 고정 조건·반복 결과는 benchmarks 보고서가 소유 |
