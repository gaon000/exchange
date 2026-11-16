# 복구 뒤 RSS 증가 진단

2026-11-13, master `24bc9955031847e637d7e1c3552043bc1ee2b686` 기준. 이전 [정규형 비교 버퍼 실험](snapshot-canonical-buffer.md)의 history/tail=0 초기 RSS 중앙값 증가(+1.266MiB)를 같은 상태에서 재확인했다. **초기 증가 경향은 다시 관찰됐지만, 유휴 시간이 지나도 후보 RSS가 계속 더 높다는 패턴은 재현되지 않았다.**

같은 확장 probe로 두 decoder·active 대조/history 대상·버전별 5회, 총20개 새 worker/260개 표본을 수집했다. history의 GC 직후 RSS 중앙값은 41.703→42.500MiB(+0.797MiB), 후보가 높은 대응 쌍은 4/5개였다. 15초 뒤에는 25.047→7.453MiB로 방향이 바뀌고 후보가 높은 쌍은1개/같은 쌍1개/낮은 쌍3개였다. 이는 계측이 다른 예전 표본과 한 집단으로 합친 수치가 아니다.

20개 worker 모두 15초 동안 live heap 증가가 0.422~0.500KiB에 그쳤고 stack 크기의 증가 경향은 보이지 않았다. 반면 Go HeapReleased·Mach reusable/compressed·physical footprint가 크게 변했다. 따라서 **이 단일 복구/유휴 구간의 RSS 차이를 live heap 누수로 해석할 근거는 없으며, runtime의 idle 페이지 처리와 OS 반환/재사용·압축 계수 및 시점의 영향이 관찰됐다.** 어느 한 함수가 이전 +1.266MiB의 유일한 원인이라고 확정하거나 일반적인 누수 부재/운영 메모리 감소를 증명한 것은 아니다.

**검증 상태:** 20회 복구와 원본/source 검증, 두 전체 로컬 CI와 새 폴더 재집계를 완료했다. 최초에는 디스크 여유 부족으로 두 CI smoke와 새 폴더 재집계가 거절돼 사용자 결정에 따라 미완료·보류로 기록했다. 이후 사용자가 공간을 확보했고 약18.7GiB 여유를 확인한 뒤 같은 기준으로 재실행해 모두 통과했다. 이전 실패 로그/보류 이력을 보존했으며 기준을 낮추거나 원본을 삭제하지 않았다.

## 조건과 방법

입력 `var/canonical-buffer-full-20261113`의 봉인435파일·전체 기존 실험을 검증했다. 같은 총1만 명령의 두 최종 상태에서 tail=0(snapshot seq=10000/재생0)만 사용한다. active 최종 주문/활성/체결은 9995/9992/1, history는4999/0/2499, 최초 결과는 각각10000개다. Genesis/runId/전체 WAL/최종 snapshot SHA가 이전과 같다. 이전 결과 중 tail=0의20개 표본을 prior.json에 별도로 보존했다.

두 decoder는 입력의 baseline/candidate source archive에서 SHA를 확인해 읽었다. 현재 Go 진단 probe를 두 빌드에 동일하게 넣고 decode.go 하나만 build overlay로 대체했다. 모든 internal/cmd 생산 source와 module/version이 원래 각 버전과 같고 측정 Go probe는 같아야 한다. baseline/current source archive는 각270파일이며 실제 컴파일한 decoder bytes·archive 모든 멤버/내용·overlay mapping·바이너리를 검증했다. 생산 파일은 수정하지 않았다.

| 버전 | 바이너리 SHA-256 |
| --- | --- |
| baseline | `98fe0d039a577cc6ba2a27ec42838df569b51d6d9d860232903bba83037ddba4` |
| candidate | `293fd49314af188f848afcaf365167b33c301538ca1942d2374457319a4fd8d7` |

M3 Pro·11 CPU·18GiB·macOS15.8 arm64/APFS·Go1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB. profiler/trace/GODEBUG는 끄고 따뜻한 사본/해시 뒤 새 프로세스를 사용했다. 상태 순서와 버전 선후를 교대해 상태별 baseline 선행은2/3회, 전체 대응10쌍에서는5/5회다. 다른 앱/호스트 메모리 압력은 격리하지 않았으며 전역 압력 계수는 이번에 수집하지 않았다.

기존 복구 뒤 GC 다음 idle 0/100ms/1/5/15초, 추가 runtime.GC 뒤0/100ms/1초, debug.FreeOSMemory 뒤0/100ms/1초를 순차 관측했다. Open 전/후도 한 번씩 기록했다. 강제 반환 자체에 GC가 포함돼 GC-only 단계를 먼저 뒀다. 각 표본은 MemStats→현재 RSS→peak→VM을 순서대로 읽으므로 원자적 snapshot이 아니다. 실제 수집 소요 최대1.850ms·목표 대비 지연 최대3.034ms를 기록했다. 추가 GC/반환의 실행 시간도 원본에 남겼다.

Runtime/엔진을 살아 있게 유지하고 모든 조회·재시도·검증용 Snapshot·종료는 관측 뒤에 수행했다. 각 worker에서 정확한 복구 경로·fallback 부재, 최초 응답8개/재시도2개·후속 체결/취소·예약금/잔고·주문/체결 cursor와 최종 canonical snapshot/전체 WAL 불변을 검사했다. 총 최초 응답160개/재시도40개가 통과했다. 기존 engine/checkpoint oracle과 독립 정수 기대값이며 독립 매칭 엔진 대조는 아니다.

## 이력형: 시간과 계수별 결과

각 항목별5회 중앙값, MiB다. 열별 중앙값은 같은 한 프로세스의 분해표가 아니므로 더해서 총량을 만들지 않는다. RSS/footprint/Go heap/reusable/compressed는 범위가 다르며 대소 관계를 강제하지 않는다.

| 버전 | 단계 | RSS | live heap | HeapReleased | physical footprint | reusable | compressed |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | idle 0s | 41.703 | 9.626 | 3.281 | 38.392 | 0.125 | 0.000 |
| baseline | idle 15s | 25.047 | 9.627 | 12.141 | 32.142 | 0.000 | 15.250 |
| baseline | gc-only 1s | 28.891 | 9.622 | 22.047 | 26.314 | 2.438 | 4.016 |
| baseline | force-release 1s | 29.062 | 9.622 | 22.805 | 23.408 | 3.859 | 3.828 |
| candidate | idle 0s | 42.500 | 9.625 | 6.641 | 38.533 | 0.000 | 0.000 |
| candidate | idle 15s | 7.453 | 9.626 | 19.984 | 29.033 | 0.000 | 25.906 |
| candidate | gc-only 1s | 21.047 | 9.621 | 26.234 | 27.236 | 0.438 | 8.953 |
| candidate | force-release 1s | 20.328 | 9.621 | 26.312 | 27.236 | 0.156 | 8.906 |

후보의 idle0→15초에서 HeapReleased는6.641→19.984MiB로 증가하고 footprint는38.533→29.033MiB로 감소했다. 동시에 compressed 계수는0→25.906MiB로 변했다. RSS 감소 전체를 페이지 반환이나 최적화 덕분이라고 해석하지 않는다. 15초 시점에는 전체20개 중16개 worker의 compressed가0보다 컸고 최대34.422MiB였다. 일부 worker는 reusable 증가와 footprint 감소 중에도 RSS가 그대로였다.

history 첫 후보 표본은 RSS41.688MiB가15초 동안 같았지만 footprint38.533→28.142MiB, reusable0→10.391MiB, HeapReleased6.641→24.172MiB로 변했다. 이후 다른 반복에서는 RSS가 한 자릿수 MiB까지 줄고 compressed가 커졌다. 추가 GC 뒤 RSS가 다시 늘어난 사례에서도 live heap은 줄고 compressed가 감소했다. 이 관측은 RSS만 보면 회수/압축/재접근의 구분을 놓칠 수 있음을 보여 주며, 모든 차이를 압축 하나로 확정하지 않는다.

초기 live heap은 baseline9.6265/candidate9.6253MiB, 15초 뒤9.6269/9.6257MiB다. stack 중앙값은 두 버전/주요 단계 모두0.656MiB였다. 새 진단의 HeapSys는 초기35.375→39.344MiB로 후보가 높았지만, retained idle(HeapIdle-HeapReleased)은21.016/21.125MiB로 비슷했다. 이전 비계측에서 HeapSys가 같았던 것과도 다르다. 가상 heap 예약량을 RSS/실사용 RAM 증가로 바꾸어 해석하지 않는다.

GC-only 뒤 후보가 높은 RSS 대응 쌍은2/5개, 강제 반환 뒤에도2/5개였다. 후보의 force-release1초 RSS 중앙값은20.328MiB로 baseline29.063MiB보다 낮지만 **footprint는27.236MiB로 baseline23.408MiB보다 높았다**. 한 계수의 낮은 값만 골라 메모리 개선을 채택하지 않는다.

## 대조 상태와 강제 반환 판단

| 상태 | 버전 | idle0 RSS | idle15s RSS | idle0 footprint | idle15s footprint | idle15s compressed |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| active | baseline | 46.297 | 6.469 | 42.720 | 26.095 | 23.734 |
| active | candidate | 45.844 | 32.297 | 42.736 | 26.329 | 1.391 |
| history | baseline | 41.703 | 25.047 | 38.392 | 32.142 | 15.250 |
| history | candidate | 42.500 | 7.453 | 38.533 | 29.033 | 25.906 |

압축/회수 변화는 이력형 후보만의 현상이 아니었다. 20개 모두 idle15초 RSS가 초기보다 증가하지 않았지만 일부는 같았고, heap의 작은 증가에는 관측 코드 자체의 할당이 포함된다. 제한된 유휴 관측이며 장기간 요청·반복 Open을 같은 프로세스에서 수행한 누수 검사는 아니다.

추가 GC 뒤1초에서 강제 반환 뒤1초까지 HeapReleased의 worker별 추가 증가 중앙값은 active 기준/후보0.094/0.031MiB, history0.141/0.117MiB였다. 강제 반환 뒤에도 RSS가 약간 오르거나 유지되는 경우가 있었다. **FreeOSMemory를 운영 경로에 도입하지 않는다.** 모든 worker가 같은 순서로 GC-only 다음 강제 반환을 하므로, 별도 무처치 arm·같은 경과 시간의 무작위 대조 없이 강제 반환의 인과 효과를 독립 추정한 것은 아니다.

## 해석 근거와 한계

Go의 [MemStats 정의](https://pkg.go.dev/runtime#MemStats)에서 HeapSys는 heap의 가상 주소 공간, HeapIdle-HeapReleased는 runtime이 보유한 idle 공간의 추정치다. [FreeOSMemory](https://pkg.go.dev/runtime/debug#FreeOSMemory)는 GC 뒤 OS 반환을 시도한다. 설치된 Go1.27.3 source에서도 이 정의를 확인했고 Darwin의 [sysUnusedOS](https://go.dev/src/runtime/mem_darwin.go)는 MADV_FREE_REUSABLE로 task_info 회계에 정보를 전달한다. 표준 설명과 측정 결과를 연결하는 해석이며 현재 runtime/OS의 반환 시점은 모든 환경에서 같지 않다.

Mach VM 필드는 로컬 SDK의 TASK_VM_INFO 구조/revision을 확인해 읽었다. Apple의 [공개 task_info 구현](https://github.com/apple-oss-distributions/xnu/blob/main/osfmk/kern/task.c)은 resident와 reusable/internal_compressed/phys_footprint를 별도 ledger로 다룬다. compressed 계수를 압축 후 실제 물리 RAM bytes라고 가정하거나 RSS에 더해 총량을 만들지 않는다. 이 공개 main source가 실행 호스트 커널의 정확한 commit과 같다고 입증한 것은 아니며 세부 원인 전체를 kernel trace로 검증한 것도 아니다.

확인된 것은 제한된 상태/유휴 창에서 live heap 증가가 거의 없고, 반환·재사용·압축 계수와 RSS/footprint가 다른 방향으로 움직인다는 점이다. 이전 +1.266MiB를 코드의 특정 allocation 하나·누수·GC 하나의 문제로 확정할 수 없다. 압축이 적은 통제된 호스트에서 같은 실험을 반복하면 남은 OS/호스트 경쟁 영향을 더 분리할 수 있다. CPU/할당 프로파일·10분 HTTP 부하·p99/최대 처리량·실제 전원 장애 검증은 하지 않았다.

## 검증 상태와 재개 조건

- 두 overlay의 statescale race 회귀·Python99개·4 worker smoke 통과.
- full20 worker/260표본 모두 시간 순서·메모리 관계·GC 단계·2GiB 메모리 budget과 전체 복구 검증 통과. 관측 최고 현재/peak RSS49.344MiB, footprint44.221MiB, heap30.883MiB다.
- 주 묶음128파일/33.92MiB, 두 source archive 각270파일·입력435파일과 전체 source/overlay·상태 hash/정리 receipt를 검증했다. 성공 소유 scratch만 정리하고 기존 원본·고정 빌드·실패는 보존했다.
- 메모리 내 analyze 결과/Markdown은 기존 원본과 일치했다. 새 폴더 `var/recovery-rss-reanalysis-resumed-20261113`의 JSON/Markdown bytes도 원본과 일치했다. 첫 공간 부족 거절은 별도 로그로 보존한다.
- 최초 native CI `var/ci/native.UZjg3D`: Go race279 tests/23 packages·Python99 통과, state-scale 진단 smoke의8GiB+128MiB 준비 여유 검사 실패. 전체 exit1.
- 최초 portable CI `var/ci/portable.CWrMq5`: Go240 tests/23 packages·Python99 통과, 진단 smoke의8GiB 여유 검사 실패. 전체 exit1.

- 재개 native CI `var/ci/native.72Fn02`: race279 tests/23 packages·명시적 helper skip1·Python99·native 진단 smoke 전부 통과, 전체 exit0.
- 재개 portable CI `var/ci/portable.jZf9Dg`: Go240 tests/23 packages·Python99·portable 진단 smoke 전부 통과, 전체 exit0.

수집은 실행 전 공간 검사들을 통과했다. 수집 뒤 잔여 검증 중 여유 공간이 약6.6GiB로 내려갔으며 사용자에게 공간 확보를 문의했다. 사용자는 당시 확보가 어렵고 CI 미완료 기록을 선택했다가 이후 공간 확보를 알려 재개했다. 공간 gate를 낮추거나 이전 근거를 삭제하지 않았다. 두 CI 로그와 첫 재집계 거절도 보존한다. 이후 8GiB+128MiB 준비 기준과 검증 임시 공간을 충분히 확보한 상태에서 두 전체 CI와 새 폴더 재집계를 완료했다.

기계 판독 [결과 JSON](20261113-recovery-rss.json)·[자료 검증/실패·재개 이력](20261113-recovery-rss-artifacts.json)·[명령과 계수](../recovery-rss.md)를 따른다. 이 진단 계획의 로컬 구현·분석·검증은 완료했다. 이 기록은 구현 완료 시점의 근거이며 당시 커밋·병합·push·원격 CI는 수행하지 않았다. 후속 커밋/발행 상태는 별도로 확인한다. 실제 OS·전원 장애는 **미완료·보류**, v1 계획도 Blocked다.
