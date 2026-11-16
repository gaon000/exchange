# 복구 뒤 RSS 증가 진단

상태: Completed
종료일: 2026-11-13
시작일: 2026-11-13
기준: master 24bc9955031847e637d7e1c3552043bc1ee2b686

## Purpose / Acceptance

정규형 비교 버퍼 최적화의 history/tail=0 RSS 증가가 같은 상태에서 반복되는지 확인한다. 보관된 두 버전의 decode.go를 같은 최신 진단 Go probe에 overlay해 두 상태(active 대조/history 대상) 각 버전 5회·20개 새 worker로 관측한다. 생산 코드는 수정하지 않는다.

GC 뒤 0/100ms/1/5/15초의 RSS·확장 MemStats를 기록하고, 별도 GC-only 및 FreeOSMemory 진단 단계 뒤 0/100ms/1초를 관측한다. 현재 RSS/누적 peak/physical footprint·reusable·compressed 등 Mach VM 값도 구분한다. 초기 API 시간/총 할당은 관찰자가 있는 진단 값으로 기존 비계측 성능과 혼합하지 않는다. 추가 GC/강제 반환을 운영 최적화로 도입하지 않는다.

원본을 봉인 검증하고 상태·최초 응답/동일 ID 재시도·후속 거래/잔고/최종 bytes를 매 worker에서 검증한다. source/archive/overlay/바이너리·원 표본·JSON/Markdown 재집계를 보존한다. 2GiB 측정 메모리/8GiB 디스크 여유/4GiB 묶음·128MiB 준비 기준 유지. 기존 원본/실패 보존, 성공한 소유 scratch만 receipt 후 정리한다.

## Progress

- [x] 현재 branch/source·보관 자료·실험 공간/계약 확인.
- [x] 확장 메모리/시간별 RSS·GC-only/강제 반환 관찰자·회귀/재현 도구. 두 decoder overlay의 statescale race·Python 99개·4 worker smoke 통과.
- [x] 두 overlay·짧은 smoke·20회 동일 상태 진단/복구 검증. 원본128파일·기준/후보각270 source 검증.
- [x] 기존/새 결과 분석·문서·메모리 내 재집계 동일성. native race279/portable240·각Python99 통과.
- [x] 사용자 공간 확보 후 native/portable 전체 CI의 진단 smoke·새 폴더 재집계 완료. 첫 실패/보류 이력과 재개 결과를 함께 보존.

## Surprises & Discoveries

- 기존 history/tail=0의 RSS 중앙값은 41.422→42.688MiB였지만 live heap은 약9.623MiB, HeapSys는39.344MiB로 같았다. 단순 heap 크기만으로 RSS 차이를 설명할 수 없다.
- HeapSys는 가상 주소 공간이며 현재 RSS가 아니다. HeapIdle/HeapReleased·stack·Mach footprint/reusable을 구분해야 한다.

## Decision Log

- 2026-11-13: 같은 진단 probe/환경에서 두 생산 decoder만 overlay로 바꾼다. 추가 메모리 관측은 모든 조회/검증용 Snapshot보다 앞에서 수행한다.
- 2026-11-13: FreeOSMemory 자체에도 GC가 포함되므로 먼저 GC-only 단계를 둔다. 관측 결과의 원인 범위를 좁히되 단일 원인/일반적 누수 부재를 확정하지 않는다.

- 2026-11-13: 수집 완료 뒤 여유 공간이 약6.6GiB로 내려가 native CI smoke와 새 폴더 재집계의 8GiB+128MiB gate가 거절했다. 사용자가 “지금은 어려워 — CI 미완료로 기록”을 선택해 당시 이 두 검증을 미완료·보류했다. 이후 공간 확보 뒤 재개해 완료했으며 이 결정은 최초 차단 이력으로 보존한다. 기준을 낮추거나 기존 근거를 삭제하지 않는다. 당시에는 메모리 안의 재집계 일치만 확인했고 새 폴더 검증은 미완료였다.

## Outcomes & Retrospective

핵심 분석·문서와 잔여 검증을 완료했다. 최초 두 CI smoke/새 폴더 재집계는 공간 부족으로 Blocked였으나 사용자가 공간을 확보한 뒤 native race279/portable240 tests·각Python99·두 진단 smoke·새 폴더 재집계를 완료했다. 구현 완료 당시 커밋·병합·푸시는 요청 범위가 아니었으며 후속 커밋/발행 상태는 별도로 확인한다. 실제 전원 장애는 미완료·보류, v1 계획 Blocked 유지.


20개 fresh worker/260표본 모두 복구/최초 응답160개/재시도40개·후속 거래/잔고/최종 상태 검증을 통과했다. history 초기 RSS 중앙값41.703→42.500MiB, 높은 대응4/5쌍이지만 idle15초25.047→7.453MiB·높은1/5쌍으로 방향이 일정하지 않았다. live heap의 idle15초 변화는 전체20개에서0.422~0.500KiB이며,16개에서 compressed>0(최대34.422MiB)을 관측했다. 반환/reusable/압축 계수 변화가 RSS/footprint와 다르게 나타나므로 특정 함수의 누수·일반적 누수 부재나 메모리 개선을 확정하지 않는다. FreeOSMemory는 진단에서만 사용했다.

원본128파일/33.92MiB·동결 source 각270개/입력435파일을 검증했고 새 report JSON/Markdown의 메모리 내 재집계는 같다. 최초 새 폴더 재집계 거절과 두 전체 CI exit1을 보존했다. 재개 조건은8GiB+128MiB 이상의 여유와 검증 임시 공간 추가 확보다. 이 조건을 확보한 뒤 두 전체 CI와 새 output의 `recovery_rss.py analyze`를 실행해 잔여 검증을 마쳤다.

[분석/검증 이력](../../benchmarks/recovery-rss.md)·[재현과 계수](../../recovery-rss.md)를 따른다. 생산 코드 변경·커밋·merge·push·원격 CI는 수행하지 않았다. 실제 전원 장애 미완료·보류 및 v1 Blocked 유지.

- 2026-11-13 재개 완료: 사용자가 공간을 확보했다. 실제 여유 약18.7GiB를 확인하고 두 전체 CI·새 폴더 재집계를 새 경로로 실행해 모두 통과했다. native `var/ci/native.72Fn02`, portable `var/ci/portable.jZf9Dg`, 재집계 `var/recovery-rss-reanalysis-resumed-20261113`이다. 이전 실패/보류 기록은 유지한다.
