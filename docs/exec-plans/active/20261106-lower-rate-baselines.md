# 낮은 요청률의 기준 확보와 비교

상태: In Progress
종료일: 미종료

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

사용자가 승인한 최근 변경 master 반영/CI, 요청률 설정화, 정상 10,000/s·snapshot 1,000/s 각 3회, 채택 가능한 작업점의 후속 비교를 진행한다. 기존 기준·host gate·실패 보존을 유지한다. 성능 개선은 자료가 뒷받침할 때만 보고하고 실제 전원 장애는 보류한다.

## Progress

- [x] c9a9950 master 머지·push.
- [x] 해당 원격 CI 확인·요청률 옵션·회귀 검증·clean producer 고정.
- [ ] 낮은 두 요청률 6회와 원본/판정 검증.
- [ ] 통과 작업점에서 비교 및 필요한 경우 병목 한 곳 후보 검증.
- [ ] 결과·한계·재현 명령과 최종 검증/커밋.

## Surprises & Discoveries

기존 order는 reference 29,000/3,000이 고정되어 있다. 기준은 최소 1,000/s 단위이며 25초 동안 최대 200만 예정 시도를 허용한다. 낮은 부하도 OS/runtime 지연에서 항상 통과한다는 보장은 없다.

## Decision Log

- 2026-11-06: --normal-rate/--snapshot-rate를 reference에만 허용하고 기본값은 유지한다. 유효성 검사와 실제 protocol/workload의 기록을 연결한다.
- 2026-11-06: warmup 5초·측정 20초·최소 3회·5초 창 20ms·실패 0·전원/quiet/예산을 완화하지 않는다. 과거 실패를 제거하거나 같은 rate의 실패 반복을 교체하지 않는다.
- 2026-11-06: producer build는 clean source로 pin하고 로컬 빌드·테스트·분석을 실제 부하와 겹치지 않는다. 개선 후보가 필요하면 기준을 확보한 후 한 곳만 검토한다.

## Outcomes & Retrospective

진행 중.

- 2026-11-06: 첫 낮은 부하 collection은 정상1/snapshot1 완료 후 snapshot2 시작 전 quiet 미달로 중단됐다. 완료 prefix를 보존하고 같은 pin/조건의 나머지만 새 root로 재개한다. 부하 시작 후 부분 실패를 교체하는 resume는 금지한다.

- 2026-11-06: OS background 분석 때문에 재개도 시작 gate에서 멈췄다. 기준을 낮추지 않고 관측 대기 횟수만 명시하는 옵션과 조건별 3회 실행 선택을 추가한다. 완료한 정상1은 기존 판정 통과, snapshot1은 WINDOW_3_P99 실패이며 해당 실패도 후속 집계에 반드시 포함한다.
