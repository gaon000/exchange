# 스냅샷 이후 명령 복구·비용 비교

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

총 10,000개 고유 명령을 고정한 active/history 각각의 동일 최종 상태에서 snapshot 이후 명령 0/1,000/5,000개 조건을 각 5회 측정한다. 같은 WAL/Genesis와 같은 runId에서 snapshot 순번만 10,000/9,000/5,000으로 다르게 선택한다. 최신 생산 코드를 그대로 쓰며 표준 도구 probe·조정기만 추가한다.

첫 두 매수 주문을 snapshot cut을 가로질러 유지하고 마지막 6개 명령에서 1 lot 체결·잔량/다른 주문 취소·기존 체결/취소 no-op를 만든다. read-only 최초 결과/현재 주문/독립 기대 잔고/거래 확인 후 같은 ID 재시도도 확인한다. 전체 canonical 최종 snapshot bytes는 모든 반복·cut에서 같아야 한다. 기존 단순 상태 실험과 명령열 구성이 달라 과거 수치를 직접 전후 성능 비교로 쓰지 않는다.

## Progress

- [x] 현재 코드·복구 계약/조회·공간 확인, feature branch 생성.
- [x] deterministic prefix snapshots·독립 산술/첫 응답 witness·소유 사본 Open 계측 구현.
- [x] 작은 실제 cut 복구/회귀·전체 CI와 동결 빌드 준비.
- [x] 6조건 각 5회·30 worker 측정/불변 파일·재집계 검증.
- [x] JSON/Markdown·한계/복구 근거 문서화.

## Decision Log

- 2026-11-13: 총 명령·WAL bytes·최종 상태를 고정하고 snapshot cut만 바꾼다. prefix 경계는 WAL 그룹 경계로 맞춘다. full은 10,000명령, smoke는 24명령/이후 0·8·12개다.
- 2026-11-13: Open의 wall/할당·GC 뒤 heap·누적 RSS와 recovery stage를 기록한다. 준비/해시/강제 GC/후속 조회·중복 재시도·Snapshot 최종 bytes 검증은 측정 밖이다. warm-cache·비격리 개발 호스트이며 HTTP/전원 장애 검증이 아니다.

## Validation and Acceptance

같은 초기 supply/전체 WAL, 정확한 snapshot seq와 replay 개수·fallback 부재, 전체 최종 상태 bytes·최초 응답/현재 주문/거래/예약 반환의 기대값·재시도 무추가 로그를 확인한다. 범위/소유권/출력 재사용·입력 변조/누락/잘못된 경로를 거절하고 파일/바이너리/소스를 검증한다. source 수정 뒤 테스트와 portable/native 전체 CI를 수행하며 4GiB 묶음/8GiB 여유 예산 안에서 원본을 보존한다.

## Outcomes & Retrospective

30회 모두 정확한 snapshot seq/재생 수·fallback 부재·전체 최종 snapshot bytes 일치·최초 응답 240건·중복 재시도 60건·후속 체결/취소/예약 반환을 확인했다. active Open 중앙값은 tail 0/1천/5천에서 297.963/281.546/194.911ms, history는 263.140/256.811/178.166ms다. 작은 snapshot load/Confirm 비용 감소가 replay 차이보다 컸다. 최적 snapshot 간격/HTTP 개선을 확정하지 않는다.

로컬 portable 232개·native race 270개(각 23 packages)·각 Python 72개와 진단 smoke가 통과했다. 24명령 실제 smoke는 6회/48 최초 응답/12 재시도를 통과했다. 최초 테스트의 임의 trade 커서 실패는 기존 실제 커서 계약에 맞춰 probe를 수정했고 실패/수정 로그를 보존했다.

본 자료 `var/snapshot-tail-full-20261113`의 봉인 파일 314개·489.91MiB, code archive 245개 member/현재 source·고정 바이너리/모든 fixture/worker를 확인했다. 새 재집계의 JSON/Markdown은 원본과 bytes까지 같다. [결과/제약](../../benchmarks/snapshot-tail-recovery.md), [도구 명령](../../snapshot-tail-recovery.md)을 따른다. 기존 원본 보존·4GiB 상한/8GiB 여유를 유지했다.

병합 전 검토에서 조정기의 HEAD 검사에 새 미추적/ignore 생산 파일 거절과 실제 임시 Git 저장소 회귀를 추가했다. 이후 Go/생산 source는 측정 archive와 모두 같고 차이는 Python 조정기·테스트 두 파일뿐이다. 원본/재집계 bytes를 다시 확인했고 새 6조건 기능 smoke도 통과했다. 비용 표본은 교체하지 않았다. 검토 뒤 전체 CI도 portable 232/native race 270·각 Python 73과 진단 smoke를 통과했다. 수정 도구 smoke의 Go 바이너리 SHA는 본 측정과 같다.

생산 코드는 master ef8cc8f와 같다. 위 기록은 구현/검토 시점의 로컬 결과이고 후속 커밋·병합·푸시와 원격 CI는 별도 확인한다. 실제 전원 장애는 미완료·보류이고 v1 전체는 Blocked다.
