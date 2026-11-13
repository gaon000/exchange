# 스냅샷 배열 증가·복사 할당 최적화

- 상태: Completed
- 종료: 2026-11-13
- 시작: 2026-11-13
- 범위: snapshot 배열의 실제 항목 기반 용량 증가. canonical leaf·파일 형식·이중 checksum·검증 순서·복구/strict sync·32KiB 읽기·16KiB leaf·2GiB payload/측정 메모리 상한 유지.
- 기준: master a43e59af22ddb3bad9582497009cbc98795a211f. 선언된 SnapshotSeq/limit만으로 미리 할당하지 않는다.

## 완료 조건

1. 이전 빌드와 후보를 source archive/해시와 함께 동결한다. 생산 변경은 internal/snapshot/decode.go 하나, 측정 Go probe는 동일해야 한다.
2. 배열 경계·과대 선언·오류 우선순위·canonical/복구 회귀를 검사한다.
3. 기존 동일 1만 명령 fixture에서 active/history × 이후 명령 0/1000/5000 × 두 버전 × 5회, 새 worker 60개를 비교한다. 느린 표본도 포함한다.
4. Open/단계 시간·TotalAlloc·GC 뒤 heap·RSS, 최초 응답·재시도·후속 체결/취소·전체 canonical 상태 일치를 확인하고 JSON/Markdown을 재집계한다.
5. 성공한 소유 scratch만 hash receipt 후 정리하고 원본·고정 빌드·실패 자료를 보존한다. 여유 8GiB/출력 4GiB 기준을 유지한다.
6. 로컬 portable/native CI, 문서와 공백 검사를 완료하고 채택 여부/한계를 기록한다. 커밋·merge·push는 이번 요청 범위가 아니다.

## 진행

- 현재 engine.Restore는 decoded 배열을 내부 map/list/별도 trade 배열로 재구성한다. 따라서 일시적인 decoder capacity와 GC 뒤 유지 heap을 구분한다.
- 실제 OS·전원 장애 검증은 미완료·보류이며 v1 전체 계획은 Blocked를 유지한다.

## 결과와 결정

- [x] 259개 source/고정 바이너리 각각 동결, 생산 차이 decode.go 하나·Go probe 동일 확인.
- [x] 실제 성공 항목 기반 배증/기존 count limit clamp. canonical/오류/메모리 경계 차등 회귀와 golden·전체 저장/복구 검사 통과.
- [x] 두 상태·세 tail·버전별 5회/60 worker 전부 상태 bytes·직접 최초 응답 480개/재시도 120개·후속 체결/취소/예약금 검증 통과.
- [x] 기존 peak RSS에 더해 Mach current resident RSS를 GC 뒤 수집. 같은 probe로 두 버전을 다시 동결하고 최종 60회 완료. 예비 60회는 별도로 보존·최종 통계 제외.
- [x] 모든 조건 TotalAlloc 중앙값 1.96~7.37% 감소, live heap 중앙값 차이 2.2KiB 이내. 현재 RSS는 조건별 혼합, 최대 중앙값 증가 +4.406MiB(+11.48%). 후보 모든 현재 RSS 48.578MiB 이하·기존 2GiB 측정 budget 유지. 할당 감소 목적으로 채택하며 메모리/시간 일괄 개선은 주장하지 않음.
- [x] 새 JSON/Markdown 재집계 bytes 동일. 주 묶음 327파일/57.84MiB·기존 314파일·두 archive 각 259 source 확인. 성공 소유 사본만 receipt 후 정리, 기존 원본/빌드/실패 보존.
- [x] 로컬 portable 236/native race 275 tests·각 Python 88·진단 smoke 통과. 배열 fuzz 1,003,225회/reader fuzz 263,341회 통과.

현재 RSS 추가 전 예비 결과·잘못된 smoke fixture 기대값·sandbox loopback/sysctl 거절·CI 실행 중 첫 재집계 공간 부족은 보존했다. 기준을 낮추거나 성능 표본을 교체하지 않고 올바른 조건·권한/새 경로에서 확인했다. [결과](../../benchmarks/snapshot-array-allocation.md)와 [도구/재현](../../snapshot-array-allocation.md)을 따른다.

이 계획의 완료 근거는 로컬 구현·측정·검증이다. 구현 완료 당시에는 커밋·병합·push·원격 CI를 수행하지 않았으며 후속 발행 상태는 별도로 확인한다. 전원 장애 미완료·보류 및 v1 Blocked 유지.

병합 전 검토: 생산 코드 수정 없이 snapshot/statescale race·Python 88개와 원본/동결 source·재집계 JSON/Markdown bytes를 재확인했다.
