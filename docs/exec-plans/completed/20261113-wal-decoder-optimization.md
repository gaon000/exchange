# WAL 전용 디코더 최적화와 동일 상태 비교

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

WAL 레코드의 두 범용 strictjson 순회/shape map을 typed decode와 기존 canonical 재인코딩·전체 bytes 대조로 대체한다. 폐쇄된 DTO·필수 필드·중복/unknown/null/순서/정수/UTF-8 거절, 의미 검증과 framing·복구·strict sync 계약은 유지한다. HTTP/Genesis/사용자 설정 입력의 범용 strictjson은 변경하지 않는다.

기존 1만 명령 active/history fixture 사본에서 WAL 전체 복구와 snapshot+전체 WAL 복구를 baseline/candidate 각각 5회 측정한다. 20쌍/40 worker의 공개 Open wall/할당·단계·상태/파일 bytes를 검증하고 새 snapshot 생성도 회귀 대조한다. CPU profile은 이 비계측 캠페인에 켜지 않는다.

## Progress

- [x] 현재 코드/계약·원본/공간 확인, feature branch 생성.
- [x] 변경 전 소스·바이너리 동결과 비교 조정기 준비.
- [x] WAL 제한 최적화·구형 decoder 차등 corpus/fuzz·복구 회귀.
- [x] 로컬 CI 후 동일 상태 5회씩 새 사본 측정/재집계.
- [x] 근거·한계·채택 판단과 문서 기록.

## Decision Log

- 2026-11-13: encodeRecord/파일 형식은 유지한다. 정규화된 canonical 입력만 허용하는 WAL 계약을 활용하고 byte 비교로 key 중복/누락/case/순서/null/escape 변화도 거절한다. DTO 형태 회귀와 기존 strict decoder reference의 차등 검증으로 이 가정을 고정한다.
- 2026-11-13: 같은 fixture·Go probe·런타임 옵션을 쓰고 반복마다 버전/조건 순서를 교대한다. 기존 원본·실패·느린 표본은 유지하며 cold-cache/HTTP/최대 처리량 결과로 확대하지 않는다. 실제 전원 장애는 미완료·보류다.

## Validation and Acceptance

기존 golden/모든 그룹 절단·완전 그룹 손상·재동기화/중복·snapshot fallback과 최초 결과 검증을 유지한다. 닫힌 schema 및 유효/악성 record corpus·구형 decoder와 같은 성공/ErrCorrupt 분류를 확인한다. differential fuzz와 reader fuzz, portable/native 전체 CI·same-state 결과/재생성을 수행한다. 신규 의존성 없이 Go/Python 표준 도구를 사용한다.

## Outcomes & Retrospective

20쌍/40 worker의 완전한 상태 bytes 일치와 입력/원본 불변, canonical 판정·차등 fuzz/복구 회귀를 확인해 전용 typed/canonical decoder를 채택했다. active/history의 WAL Open 중앙값은 각각 73.22%/60.23%, snapshot Open은 26.53%/26.64% 감소했다. Open 할당은 WAL 약 57.5%, snapshot 약 36.5~38.0% 줄었다. 새 snapshot 생성의 active +6.75%/history +0.88%와 느린 표본도 포함하며 생성/HTTP 처리량 개선은 주장하지 않는다. 전원 장애 검증을 필수로 둔 v1 전체 상태는 Blocked다.


## 실제 검증과 보존

- reference decoder 본문이 master ec420e0와 같고 기존 Golden/WAL 그룹 절단·checksum/semantic 손상·순번/중복·실제 저장/재동기화·fallback·프로세스/HTTP 복구·최초 결과 검증을 통과했다.
- 차등 fuzz 464,584회 및 reader fuzz 2,376,063회(각 30초 설정) 통과. 로컬 portable 226개/native race 264개, 각 Python 66개·진단 smoke 통과. `var/ci/portable.QL3JTa`, `var/ci/native.E2sCo5`, `var/wal-decoder-*-20261113.log`에 원본을 남겼다.
- 동결 baseline/candidate는 `var/wal-decoder-baseline-20261113`/`var/wal-decoder-candidate-20261113`. 생산 차이는 internal/wal/codec.go 한 파일이고 측정 Go probe/toolchain은 같다. 코드 234/236개의 archive 및 SHA를 보존한다.
- `var/wal-decoder-full-20261113`의 40 worker/383개 파일 봉인과 입력 `var/state-scale-full-20261113`의 588개 파일 봉인을 확인했다. 456MiB 출력·종료 여유 약 11GiB로 4GiB/8GiB 예산 안이다.
- `var/wal-decoder-reanalysis-20261113`에서 재생성한 JSON/Markdown bytes가 같았다. 최종 proof는 `var/wal-decoder-final-proof-20261113.json`이다. 느린 표본을 보존하며 baseline을 과거 다른 시점 수치로 대체하지 않았다.
- [결과 보고](../../benchmarks/wal-decoder-optimization.md), [JSON](../../benchmarks/20261113-wal-decoder-comparison.json), [artifact index](../../benchmarks/20261113-wal-decoder-artifacts.json), [동결·재집계 명령](../../wal-decoder-comparison.md)에 연결한다. 실제 전원 장애는 미완료·보류다. 위 구현 완료 시점의 검증은 로컬 결과이며 후속 커밋·병합·푸시와 원격 CI는 별도로 확인한다.
