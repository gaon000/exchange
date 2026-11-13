# 스냅샷 배열 증가·복사 할당 최적화

2026-11-13, master `a43e59af22ddb3bad9582497009cbc98795a211f`의 생산 코드를 기준으로 `internal/snapshot/decode.go`의 배열 증가만 바꿨다. **총 할당 감소 목적으로 채택한다.** 여섯 조건 모두 Open TotalAlloc 중앙값이 1.96~7.37% 줄고 GC 뒤 live heap 차이는 중앙값 기준 2.2KiB 이내였다. Open 시간 변화는 -1.59~+0.82%로 혼합됐으며 속도 개선으로 채택한 것은 아니다.

**RSS 감소는 달성하지 않았다.** GC 뒤 현재 RSS 중앙값은 active tail 0/5천에서는 0.688/1.625MiB 감소했지만 나머지 네 조건은 증가했다. history tail 1천의 38.375→42.781MiB(+4.406MiB, +11.48%)가 가장 큰 증가다. 후보의 모든 현재 RSS는 48.578MiB 이하, Open peak RSS는 48.562MiB 이하로 기존 측정 2GiB budget을 유지했다. heap이 같아도 Go가 확보한 arena/stack/native resident 페이지가 같지는 않다. RSS 차이의 원인을 배열 capacity·GC·allocator·호스트 중 하나로 단정하지 않는다.

## 변경과 메모리 경계

기본 append에 맡기던 backing storage 확장을 명시적인 배증으로 바꿔 큰 결과/주문 배열의 반복 복사를 줄였다. 이 함수는 계정·체결·호가/FIFO 배열에서도 공유한다. 다음 항목의 canonical 검증이 성공한 다음에만 증가하고, 기존 개수 제한을 넘지 않게 최종 증가를 자른다. 선언된 SnapshotSeq/limit만 보고 예약하지 않는다. 빈 배열은 non-nil `[]`를 유지한다.

실제 성공 항목 N>0일 때 반환 capacity는 2N 이내, 증가 순간 기존/새 storage 합은 3N 이내다(allocator 정렬·GC가 아직 반환하지 않은 공간 제외). 이는 메모리의 새 고정 byte 상한을 추가했다는 뜻이 아니다. 16KiB leaf·깊이 64·계정 수 1만·WAL 순번 기반 개수/2GiB payload 제한을 유지하며, 파일 상한이 전체 상태 메모리 가용성을 보장하지 않는 기존 계약도 같다. GOMEMLIMIT=2GiB는 Go soft limit이고 측정기는 heap/현재·최대 RSS에 별도 2GiB 초과 거절을 적용한다.

header/format/canonical bytes·오류 우선순위·이중 SHA-256·EOF/변경 reader·검증 순서·Restore 의미 검사·전체 WAL prefix/최초 응답 대조·fallback·strict sync와 읽기 32KiB는 바꾸지 않았다. JSON leaf 검증/재직렬화나 per-item DTO 할당은 이번 변경에서 최적화하지 않았다.

## 동일 입력·빌드·방법

`var/snapshot-tail-full-20261113`의 두 상태를 그대로 사용했다. 총 10,000명령, prefix snapshot seq 10,000/9,000/5,000에 대해 tail 0/1,000/5,000이다. 두 상태의 Genesis/runId/전체 WAL/최종 snapshot은 tail 간 같고 각 worker는 prefix snapshot 하나만 넣은 새 사본으로 시작한다. active 최종 주문/활성/체결은 9,995/9,992/1, history는 4,999/0/2,499이며 결과는 각각 10,000개다.

Apple M3 Pro·논리 CPU 11·18GiB·macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB. 조건 순서를 회전·반전하고 대응 버전 순서를 교대했다. profile/IO observer·유휴 gate·cache flush 없는 warm-cache 비격리 개발 호스트다. 각 조건/버전 5회, 새 worker 60개이며 느린 표본도 전부 포함했다. 준비/해시/강제 GC·후속 검증/종료는 Open wall 밖이다. 자연 GC와 Open 내부 I/O 대기는 포함한다.

계측 Go probe는 두 빌드에서 동일하다. GC 직후 현재 RSS를 Mach resident_size로 추가한 뒤 기준 생산 소스를 기존 동결 archive에서 잠시 복원해 같은 probe로 baseline을 다시 동결했고 후보 파일은 즉시 복원했다. 각 259개 source 멤버/내용 hash와 바이너리를 보존했으며 생산 차이는 decode.go 하나다. 기준 생산 소스는 fixture 생성 시점과도 같다. 두 빌드는 uncommitted probe/도구를 포함하므로 revision만으로 재현했다고 보지 않는다.

| 버전 | 바이너리 SHA-256 |
| --- | --- |
| baseline | `59051e9cc2884d99ac918cc5b7931b45222b1769c01de95d089646f366af69d6` |
| candidate | `94e1e103c99da03307712158c22f980740ae6fca77b1637734dda071cd1ab05f` |

## 관측 결과


각 버전 5회, 60 worker. 중앙값 [최소~최대].

| 상태 | 이후 명령 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 TotalAlloc MiB | 이후 TotalAlloc MiB | 변화 % |
| --- | ---: | --- | --- | ---: | --- | --- | ---: |
| active | 0 | 284.082 [278.705~294.308] | 281.986 [278.505~298.543] | -0.74 | 114.232 [114.214~114.237] | 105.818 [105.816~105.876] | -7.37 |
| active | 1000 | 266.458 [259.018~291.328] | 266.965 [263.600~272.551] | 0.19 | 106.217 [106.203~106.225] | 101.351 [101.325~101.356] | -4.58 |
| active | 5000 | 180.676 [176.640~182.274] | 182.160 [174.920~191.139] | 0.82 | 82.423 [82.405~82.469] | 79.441 [79.424~79.479] | -3.62 |
| history | 0 | 248.292 [245.939~252.318] | 244.346 [238.870~254.532] | -1.59 | 97.281 [97.254~97.302] | 92.194 [92.164~92.203] | -5.23 |
| history | 1000 | 231.428 [228.894~235.975] | 232.404 [223.044~241.676] | 0.42 | 91.313 [91.293~91.324] | 88.907 [88.883~88.924] | -2.63 |
| history | 5000 | 157.871 [149.549~161.163] | 158.740 [156.257~162.914] | 0.55 | 71.893 [71.882~71.899] | 70.484 [70.456~70.510] | -1.96 |

| 상태 | 이후 명령 | 이전 GC 뒤 heap MiB | 이후 GC 뒤 heap MiB | 이전 GC 뒤 RSS MiB | 이후 GC 뒤 RSS MiB | 이전 peak RSS MiB | 이후 peak RSS MiB |
| --- | ---: | --- | --- | --- | --- | --- | --- |
| active | 0 | 10.914 [10.912~10.914] | 10.912 [10.912~10.913] | 45.781 [44.562~47.047] | 45.094 [44.469~48.578] | 45.750 [44.562~46.922] | 45.078 [44.469~48.562] |
| active | 1000 | 10.859 [10.856~10.861] | 10.859 [10.857~10.865] | 42.203 [41.281~43.469] | 43.344 [42.234~45.641] | 42.141 [41.172~43.469] | 43.281 [42.062~45.641] |
| active | 5000 | 10.658 [10.657~10.675] | 10.657 [10.656~10.663] | 36.266 [34.594~36.891] | 34.641 [33.781~36.719] | 36.094 [34.594~36.891] | 34.594 [33.781~36.703] |
| history | 0 | 9.624 [9.621~9.624] | 9.622 [9.621~9.625] | 40.484 [40.125~43.547] | 41.609 [40.344~42.359] | 40.484 [40.125~43.547] | 41.609 [40.344~42.359] |
| history | 1000 | 9.646 [9.644~9.650] | 9.644 [9.644~9.646] | 38.375 [37.625~39.234] | 42.781 [38.531~44.219] | 38.375 [37.609~39.219] | 42.750 [38.484~44.203] |
| history | 5000 | 9.504 [9.503~9.524] | 9.503 [9.491~9.507] | 32.453 [31.109~34.203] | 32.750 [32.344~34.406] | 32.422 [31.078~34.141] | 32.734 [32.312~34.234] |

준비/해시/강제 GC/후속 검증은 Open wall 밖이다. 자연 GC·내부 I/O 대기는 포함한다.
heap은 엔진을 살려 둔 강제 GC 뒤 HeapAlloc, GC 뒤 RSS는 Mach resident_size, peak RSS는 worker 시작부터 Open 직후까지 high-water다. 2GiB budget 유지.
동일 입력·32KiB decoder·canonical/이중 checksum·WAL prefix/최초 응답/전체 상태·strict sync 유지. 느린 표본도 포함.
warm-cache·비격리 호스트이며 HTTP p99/최대 처리량 개선을 증명하지 않는다. 실제 OS·전원 장애 검증은 미완료·보류다.

Open이 baseline보다 빨랐던 대응 쌍은 active tail 0/1천/5천에서 3/2/1개, history는 4/2/1개였다. 여섯 조건 중 시간 중앙값이 낮은 것은 두 조건이며 모든 조건의 속도 향상을 주장하지 않는다. load/restore/replay/confirm의 최소·중앙·최대와 모든 대응 변화율은 [JSON](20261113-snapshot-array-allocation.json)에 남겼다. 위 RSS 값은 현재 RSS와 누적 peak의 샘플 시점이 다르므로 서로 대소 관계를 강제하지 않는다. physical footprint·compressed memory·swap 계측은 아니다.

## 복구·도구 검증

60개 모두 정확한 선택 snapshot/재생 수와 fallback 부재를 확인했다. 직접 최초 응답 480개와 동일 ID 재시도 120개, 후속 체결·잔량 취소·예약금 반환/잔고·주문/체결 cursor 기대값과 최종 canonical snapshot SHA-256/전체 WAL 불변이 일치했다. 최종 snapshot bytes 대조는 결과 1만 개를 포함한 전체 상태에 적용했다. 기존 engine/checkpoint의 복구 oracle과 독립 정수 기대값을 사용하며 독립 매칭 엔진과의 비교는 아니다.

배열 경계 0/1/2/3/7/8/9/127/128/129/255/256/257/511/512/513/4095/4096/4097/9999/10000, 짧은 실제 배열+MaxInt64 선언 제한, 음수/0 제한·손상/비정규 JSON·포인터 DTO를 이전 parser와 비교했다. 값·정확한 오류 문자열·남은 입력과 실제 항목 기반 capacity를 검사했다. 기존 golden/전체 손상/reader 오류 및 실제 프로세스·HTTP·WAL/snapshot 복구 회귀도 전체 CI에 포함됐다.

- 로컬 native race CI: `var/ci/native.KAw1p4`, 23 packages/275 tests, 명시적 자식 helper skip 1, Python 88·진단 native smoke 통과.
- 로컬 portable CI: `var/ci/portable.2UdlwG`, 23 packages/236 tests, Python 88·진단 portable smoke 통과.
- 새 배열 차등 fuzz 30초 1,003,225회, 기존 SnapshotReader fuzz 30초 263,341회 통과. 같은 캐시의 기존 corpus도 포함한다.
- 새 폴더 재집계 `var/snapshot-array-resident-reanalysis-v2-20261113`의 JSON/Markdown bytes가 원 보고서와 일치한다. 묶음 327개 파일/57.84MiB, 두 archive 각 259 source와 기존 입력 314개 파일을 재검증했다.

주요 원본은 `var/snapshot-array-resident-full-20261113`, source/archive/환경/해시·요약은 [자료 검증 JSON](20261113-snapshot-array-allocation-artifacts.json), 실행 방법은 [도구 안내](../snapshot-array-allocation.md)다. sourceMatchesCurrent는 이번 구현 완료 시점의 확인이다. 원본/공통 fixture/고정 빌드/실패는 보존하고 성공한 새 scratch 60개만 파일 hash/size·소유 marker 확인 후 receipt를 남기고 정리했다. 실행 당시 사본 검증과 바이너리를 재실행하지 않는 재집계를 구분한다.

현재 RSS 추가 전 별도의 60회 예비 비교와 짧은 smoke도 보존했다. 예비 수치는 최종 5회 통계에 합치지 않았다. 최초 Python fixture 테스트의 잘못된 5파일 기대값(tail 0은 4파일), sandbox의 loopback/sysctl 제한, CI 실행 중 공간 부족으로 거절된 첫 재집계도 기록을 보존하고 수정/권한 적용 또는 임시 공간 회수 뒤 새 경로에서 확인했다. 성능 worker 실패나 느린 표본을 교체한 것은 아니다.

위 검증은 구현 완료 시점의 로컬 실행 근거다. 당시에는 커밋·merge·push·원격 GitHub CI를 실행하지 않았으며, 후속 발행의 실제 상태는 해당 커밋과 CI 실행으로 별도 확인한다. 동시 거래 HTTP p99·최대 지속 처리량 및 실제 OS·전원 장애 검증을 증명하지 않으며, 전원 장애는 **미완료·보류**, v1 전체 계획은 Blocked를 유지한다.

병합 전 검토에서 생산 코드 추가 변경 없이 snapshot/statescale race 회귀와 Python 88개, 동결 후보 source 259개·원본 327개/입력 314개 파일 및 JSON/Markdown 재집계 bytes 일치를 다시 확인했다. RSS 혼합 결과를 포함한 채택 범위와 전원 장애 보류는 유지한다.
