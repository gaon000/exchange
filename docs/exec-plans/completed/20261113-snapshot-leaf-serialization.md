# 스냅샷 항목 직렬화 임시 할당 최적화

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

스냅샷 항목마다 json.Marshal 반환 bytes를 만들던 경로를 재사용하는 표준 JSON Encoder와 마지막 개행만 제거하는 writer로 바꾼다. 필드 순서·escaping·정수/nullable 값·오류/크기 한도·전체 envelope bytes·strict 저장/확인/복구 계약을 유지한다. 외부 production dependency는 추가하지 않는다.

최신 WAL 최적화를 포함한 baseline을 먼저 동결하고 동일 1만 명령 active/history에서 새 생성 전후 각 5회와 두 복구 경로 전후 각 5회를 비교한다. 20쌍/40 worker의 전체 snapshot/WAL/Genesis/lock bytes 일치를 확인하고 느린 표본도 남긴다. 전원 장애는 미완료·보류다.

## Progress

- [x] 현재 코드·표준 Encoder 구현/계약·원본/여유 공간 확인, feature branch 생성.
- [x] 비교 도구의 허용 생산 변경 경로/출력 확장과 baseline 동결.
- [x] 표준 Encoder 재사용·독립 Marshal byte/error/limit 차등·실제 저장/복구 검증.
- [x] 전체 CI 후 같은 입력 전후 각 5회 측정/새 보고서 재생성.
- [x] 채택 판단·자료/한계/검증 근거 문서화.

## Decision Log

- 2026-11-13: JSON escaping/nullable/custom Marshaler/오류 wrapping을 새로 구현하지 않고 기존 Go 표준 Encoder의 buffer를 사용한다. Encode가 완전한 값을 만든 뒤 writer를 호출하는 현재 Go 구현과 bytes/error 차등 검증을 확인한다. 기존 preflight는 그대로 유지한다.
- 2026-11-13: 기존 비교 조정기를 기본 WAL 동작/JSON 출력을 바꾸지 않고 허용 생산 파일/Markdown 렌더러 인자로 재사용한다. 새 wrapper는 snapshot codec 하나만 생산 차이로 허용하고 측정 probe/toolchain 동일성을 요구한다.

## Validation and Acceptance

기존 golden·부분 쓰기/실패·16KiB/2GiB preflight·게시/동일 파일 확인·snapshot decode/복구/fallback/프로세스 경계를 유지한다. json.Marshal와 실제 leaf bytes/오류·길이 한도/경계를 차등 검사하고 fuzz한다. portable/native 전체 CI와 작은 동결 빌드 smoke 뒤 비계측 40 worker를 실행하며 기존 원본/새 원본/동결 source·해시/재집계를 검증한다.

## Outcomes & Retrospective

새 생성 할당 중앙값은 active 30.376→20.461MiB(-32.64%), history 24.932→16.384MiB(-34.28%)로 감소해 표준 Encoder 재사용 경로를 채택했다. 40 worker 모두 파일/전체 복구 bytes가 같았다. 시간 중앙값은 -4.94%/-1.24%였으나 active 1쌍/history 2쌍은 느렸고 전후 범위가 겹쳐 안정적인 latency/HTTP 처리량 개선은 주장하지 않는다. v1 전체는 물리 전원 장애 미완료로 Blocked다.


## 실제 검증과 보존

- 독립 json.Marshal 기준의 leaf bytes·type/message/도메인 오류·16KiB/잘못된 UTF-8/escaping/null/지원외 입력, 재사용/short write/IO 오류와 고정 seed DTO 4,000개를 검증했다. 초기 NaN 오류 객체 DeepEqual 비교 실패는 테스트의 비교 기준을 올바르게 고치고 원본 로그를 보존했다.
- Marshal 차등 fuzz 23,409회·reader fuzz 686,317회(각 30초 설정) 통과. 로컬 portable 229개/native race 267개·각 Python 68개와 실제 게시/프로세스·HTTP/복구/진단 smoke를 통과했다. `var/ci/portable.RoCNKg`, `var/ci/native.Ijh9De`, `var/snapshot-leaf-*-20261113.log`에 보존했다.
- baseline/candidate는 `var/snapshot-leaf-baseline-20261113`/`var/snapshot-leaf-candidate-20261113`에 source archive 237/239개와 binary hash로 동결했다. 같은 생산 기준 dc8635f에서 snapshot codec 한 파일만 바뀌고 측정 Go probe/toolchain은 같다.
- `var/snapshot-leaf-full-20261113`: 20쌍/40 worker·383 파일 봉인/동일 snapshot bytes, 입력 `var/state-scale-full-20261113` 588개 파일 봉인, candidate 현재 source와 archive 일치. 약 456MiB·종료 여유 약 11GiB로 4GiB/8GiB 예산 안이다.
- 새 폴더 `var/snapshot-leaf-reanalysis-20261113`의 JSON/Markdown bytes가 원본과 같다. 원본/이전 비교를 덮어쓰거나 삭제하지 않았다. 최종 proof는 `var/snapshot-leaf-final-proof-20261113.json`이다.
- [결과 보고](../../benchmarks/snapshot-leaf-serialization.md), [JSON](../../benchmarks/20261113-snapshot-leaf-comparison.json), [artifact index/개별 쌍](../../benchmarks/20261113-snapshot-leaf-artifacts.json), [동결/실행 명령](../../snapshot-leaf-comparison.md)을 따른다. 위 구현 완료 시점의 검증은 로컬 결과이며 후속 커밋·병합·푸시와 원격 CI는 따로 확인한다. 실제 전원 장애는 계속 미완료·보류다.
