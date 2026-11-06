# 낮은 요청률의 기준 확보와 비교

상태: In Progress
재개: 사용자가 부하를 줄인 뒤 계속 진행하도록 요청했다. 같은 조건으로 남은 네 실행을 완료하고 원본/참조 무결성을 검증했다.
종료일: 미종료

[운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

사용자가 승인한 최근 변경 master 반영/CI, 요청률 설정화, 정상 10,000/s·snapshot 1,000/s 각 3회, 채택 가능한 작업점의 후속 비교를 진행한다. 기존 기준·host gate·실패 보존을 유지한다. 성능 개선은 자료가 뒷받침할 때만 보고하고 실제 전원 장애는 보류한다.

## Progress

- [x] c9a9950 master 머지·push.
- [x] 해당 원격 CI 확인·요청률 옵션·회귀 검증·clean producer 고정.
- [x] 낮은 두 요청률 6회와 원본/판정 검증. 정상 10,000/s는 3/3 통과·qualified, snapshot 1,000/s는 2/3 통과·미채택.
- [ ] 통과 작업점에서 비교 및 필요한 경우 병목 한 곳 후보 검증.
- [ ] 결과·한계·재현 명령과 최종 검증/커밋.

## Surprises & Discoveries

기존 order는 reference 29,000/3,000이 고정되어 있다. 기준은 최소 1,000/s 단위이며 25초 동안 최대 200만 예정 시도를 허용한다. 낮은 부하도 OS/runtime 지연에서 항상 통과한다는 보장은 없다.

## Decision Log

- 2026-11-06: --normal-rate/--snapshot-rate를 reference에만 허용하고 기본값은 유지한다. 유효성 검사와 실제 protocol/workload의 기록을 연결한다.
- 2026-11-06: warmup 5초·측정 20초·최소 3회·5초 창 20ms·실패 0·전원/quiet/예산을 완화하지 않는다. 과거 실패를 제거하거나 같은 rate의 실패 반복을 교체하지 않는다.
- 2026-11-06: producer build는 clean source로 pin하고 로컬 빌드·테스트·분석을 실제 부하와 겹치지 않는다. 개선 후보가 필요하면 기준을 확보한 후 한 곳만 검토한다.

## Outcomes & Retrospective

완료한 항목은 c9a9950 master 머지/push/실제 CI, 요청률 설정화·원본 보존 resume·조건 선택/대기 횟수 설정과 회귀 검증이다. 최초 두 실행과 gate 실패는 보존했다. 사용자 재개 요청 뒤 var/lower-rate-resumed-3에서 나머지 네 실행을 모두 유효하게 완료했고 parent inventory/dependency와 6회 판정을 검증했다. 정상 10,000/s는 3/3 통과해 작업점으로 채택하며 snapshot 1,000/s는 최초 WINDOW_3_P99 실패를 포함해 2/3 통과여서 미채택이다.

테스트 전용 Int JSON prototype은 동일 bytes/오류와 5회 단위 측정의 할당 감소를 확인했다. 정상 반복 기준을 확보해 이 후보 하나를 실제 코드에 적용하고 같은 정상 10,000/s·독립 새 run 3회로 비교한다. 직접 정수 bytes를 쓰며 JSON 문자열·음수 오류·decode 계약을 유지한다. 생산 빌드와 생성기는 같은 clean revision에서 고정하므로 전체 프로그램 비교이며 서버만의 기여를 분리하지 않는다. p99·CPU·RSS 관찰을 보고하고 최대 처리량 향상은 주장하지 않는다. [진행 기록](../../benchmarks/lower-rate-progress.md)에 원본 경로·미완료·재개 명령을 남겼다.

- 2026-11-06: 첫 낮은 부하 collection은 정상1/snapshot1 완료 후 snapshot2 시작 전 quiet 미달로 중단됐다. 완료 prefix를 보존하고 같은 pin/조건의 나머지만 새 root로 재개한다. 부하 시작 후 부분 실패를 교체하는 resume는 금지한다.

- 2026-11-06: OS background 분석 때문에 재개도 시작 gate에서 멈췄다. 기준을 낮추지 않고 관측 대기 횟수만 명시하는 옵션과 조건별 3회 실행 선택을 추가한다. 완료한 정상1은 기존 판정 통과, snapshot1은 WINDOW_3_P99 실패이며 해당 실패도 후속 집계에 반드시 포함한다.

## 검증 기록

- Python 일반/`-O` 각각 27개, portable Go 206개·native race 238개와 기존 실제 smoke 통과. `var/lower-rate-resume-ci-{portable,native}.log`에 보존.
- producer pin 086c61a, 실제 prefix 두 개와 세 parent inventory/dependency를 검증했다. 마지막 root는 `var/lower-rate-resumed-2`이며 부하가 시작되지 않은 quiet 중단에서만 재개 가능하다.
- benchmark prototype은 CPU1·500ms·5회, 현재 130.8ns/80B/4alloc 중앙값과 후보 20.50ns/24B/1alloc을 관찰했다. 단일 19자리 양수의 미시 측정이며 서버 개선으로 주장하지 않는다.

- 2026-11-06: 후속 후보는 Int.MarshalJSON의 중첩 json.Marshal 제거 한 곳으로 제한한다. 정상 10,000/s에서 5초 warmup·20초 본 측정·3회·같은 gate/runtime/criteria로 실행한다. 모든 실패를 포함하고 후보가 반복 기준을 못 넘으면 채택하지 않는다. 스냅샷 실패 그룹은 개선 비교의 기준으로 사용하지 않는다.
