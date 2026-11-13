# WAL 전용 디코더 최적화 비교

2026-11-13, 기존 canonical WAL 검증·복구 계약을 유지하면서 두 번의 범용 strictjson token/shape 검사를 typed JSON 복원과 기존 전체 canonical bytes 대조로 대체했다. **같은 10,000명령 active/history 상태의 WAL/snapshot 복구에서 이전·후보 각각 5회(20쌍/40 worker)를 비교했고 전체 상태 bytes가 일치해 후보를 채택한다.** 고정 상태 복구의 개선이며 HTTP 지연/최대 지속 처리량의 개선은 검증하지 않았다.

[실행/동결/재집계](../wal-decoder-comparison.md), [원본 집계 JSON](20261113-wal-decoder-comparison.json), [동결 빌드/fixture 해시](20261113-wal-decoder-artifacts.json)를 따른다. 실제 전원 장애 검증은 **미완료·보류**다.

## 변경과 검증 계약

`internal/wal/codec.go`의 record 해석만 변경했다. 앞에서 유효 UTF-8을 검사하고 바깥 commandWire/내부 Place 또는 Cancel을 각각 json.Unmarshal로 읽는다. 기존 encodeRecord가 순번·runId·ID·종류·가격/수량 등 의미를 검증하고, 만든 canonical JSON 전체와 입력이 완전히 같을 때만 반환한다. 반복적인 JSON Token 객체·seen/shape/RawMessage map·reflect 필드 검사와 재복원을 줄였다. payload RawMessage와 canonical 재인코딩의 중간 bytes 자체는 여전히 남는다.

이 방식은 scalar 필수 DTO와 이미 요구되는 canonical 파일 형식에만 적용한다. 전체 bytes 일치가 중복/누락/unknown/case-folded 필드·순서·공백·null·alternate escape/정수 표기를 함께 거절한다. DTO schema 회귀는 map/slice/interface/embedded/omitempty 등이 새로 들어오면 이 가정을 다시 검토하도록 고정한다. 잘못된 레코드의 ErrCorrupt 분류와 거절·복구 판정이 기존과 같아야 하며 내부 오류 설명 문자열의 일치를 계약으로 추가하지 않는다.

Config/Genesis/HTTP의 범용 strictjson, encodeRecord/온디스크 버전·golden bytes는 변경하지 않았다. 4KiB record/그룹 상한, checksum/framing·그룹 원자성·연속 순번/중복/계정 membership, EOF tail/재동기화, snapshot prefix의 최초 입력 대조·fallback·strict file/directory sync는 기존 경로를 유지한다. 외부 production dependency를 추가하지 않았다.

## 비교 조건과 원본

- 기준 master는 `ec420e0b9961cf724c9145ef3bb4f61762d8e99d`다. baseline은 production 코드를 바꾸기 전에 동결했고 candidate는 검증한 작업 트리에서 동결했다. 둘 다 revision은 같고 workingTreeModified=true이며, 실제 source/archive/binary SHA로 차이를 구분한다. baseline은 새 조정기 파일 때문에 dirty이고 production은 해당 HEAD와 같았다.
- 두 동결 빌드의 production `.go`/Go 설정 차이는 `internal/wal/codec.go` 하나다(테스트 제외). 측정 Go probe source와 toolchain은 같다. baseline/candidate의 code·script·config source 234/236개 archive를 검증했고 candidate source는 본 측정 내내 같았다.
- baseline binary SHA: `accf5941c52d8e57475760ec89286630449477478b301ef58f1fc5f169c39d04`; candidate: `c0091b723a12a3d77f5d840c02faff85c3c1acbde5aa06deacc8982412a5289c`.
- Apple M3 Pro·11 CPU·18GiB, macOS 15.8 arm64/APFS·Go 1.27.3/cgo=1, GOMAXPROCS=11/GOGC=100/GOMEMLIMIT=2GiB. warm-cache이며 cache flush나 CPU 유휴 gate를 사용한 격리 호스트가 아니다. 전체 CI/fuzz와 별도 microbenchmark가 끝난 뒤 본 캠페인을 실행했다.
- 입력은 `var/state-scale-full-20261113`의 같은 1만 명령 fixture 두 개다. active는 주문/활성 10,000/10,000·체결 0, history는 주문/활성 5,000/0·체결 2,500·최초 결과 10,000개다. 초기 잔고·runId·WAL/Genesis/lock와 기준 snapshot bytes를 반복/버전마다 그대로 복사했다.
- WAL 전체 복구와 최종 snapshot+전체 WAL 복구를 각각 측정한다. snapshot tail=0이지만 보존 prefix 검사는 유지한다. wal.Open의 첫 전체 scan 및 runtime 재생/prefix 대조 scan 모두 전용 decoder를 사용한다. 공개 Open 전체 시간은 replay stage보다 넓다.
- 반복마다 버전 순서를 baseline→candidate/반대로 바꾸고 상태/복구 조건 순서도 교대했다. 20쌍 전부를 포함하며 준비/복사/해시/강제 GC/검증·shutdown은 공개 호출 wall 밖이다. 호출 내부 자연 GC·I/O 대기는 포함된다. CPU/할당 profiler는 켜지 않았다.

## 실제 5회 결과

각 버전 5회 중앙값 [최소~최대], 40 worker 상태 일치.

| 유형 | 복구 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 할당 MiB | 이후 할당 MiB | 변화 % |
| --- | --- | --- | --- | ---: | --- | --- | ---: |
| active | wal | 313.848 [196.941~358.732] | 84.047 [78.872~98.598] | -73.22 | 124.313 [124.281~124.357] | 52.849 [52.832~52.875] | -57.49 |
| history | wal | 177.651 [169.060~196.854] | 70.644 [66.899~133.401] | -60.23 | 112.716 [112.707~112.725] | 47.763 [47.759~47.786] | -57.63 |
| active | snapshot | 420.126 [415.817~487.099] | 308.646 [299.216~324.531] | -26.53 | 195.571 [195.547~195.605] | 124.153 [124.148~124.174] | -36.52 |
| history | snapshot | 362.754 [355.364~503.025] | 266.106 [253.397~273.686] | -26.64 | 170.743 [170.717~170.763] | 105.834 [105.806~105.839] | -38.02 |

| 유형 | 복구 | 이전 replay ms | 이후 replay ms |
| --- | --- | --- | --- |
| active | wal | 133.891 [103.174~148.307] | 42.743 [42.415~50.986] |
| history | wal | 90.186 [86.404~95.637] | 37.421 [36.894~65.341] |
| active | snapshot | 96.987 [95.242~115.625] | 37.485 [35.315~42.466] |
| history | snapshot | 82.638 [79.862~153.011] | 31.456 [30.774~32.974] |

| 유형 | 이전 새 snapshot ms | 이후 새 snapshot ms |
| --- | --- | --- |
| active | 72.512 [64.125~81.928] | 77.403 [60.733~120.066] |
| history | 59.629 [57.988~63.743] | 60.156 [58.765~63.233] |

전체 WAL prefix 검사·canonical 비교·strict sync 유지. 준비/복사/강제 GC/검증은 공개 호출 시간 밖이다.
새 worker·같은 입력·warm-cache·비격리 호스트이며 느린 표본도 포함한다. HTTP p99/최대 처리량 개선으로 확대하지 않는다.
실제 OS·전원 장애 검증은 미완료·보류다.

## 결과 해석과 채택

복구 시간 중앙값은 active WAL 73.22%, history WAL 60.23%, active snapshot 26.53%, history snapshot 26.64% 감소했다. 대응하는 20쌍 모두 candidate Open이 더 짧았다. 양쪽 WAL scan에서 없앤 반복 JSON 순회·map 할당의 효과가 snapshot tail 0에서도 남는다는 코드 경로/할당 근거와 일치한다. snapshot load/Restore/canonical confirm 비용은 그대로 남기 때문에 전체 snapshot 복구 개선을 WAL 경로와 같은 비율로 기대하지 않는다.

Open의 총 할당 중앙값 감소는 WAL 약 57.49~57.63%, snapshot 약 36.52~38.02%다. 반면 GC 뒤 유지 heap은 active WAL 10.127→10.109MiB, history WAL 8.957→8.950MiB, active snapshot 10.642→10.638MiB, history snapshot 9.354→9.352MiB로 거의 같다. 전체 주문/잔고/최초 결과 이력을 지운 효과가 아니라 일시적인 JSON 검증 객체를 줄인 결과로 해석한다.

느린 표본을 숨기지 않는다. active baseline WAL은 196.941~358.732ms로 범위가 넓고, history candidate WAL에도 133.401ms 표본이 있다. 신규 snapshot 생성 중앙값은 active 72.512→77.403ms(+6.75%), history 59.629→60.156ms(+0.88%)로 높아졌다. active candidate 생성 최대 120.066ms도 포함했다. Snapshot/encoder 코드 자체는 변경하지 않았으며 생성 속도 개선이나 일반적인 end-to-end 향상을 주장하지 않는다. 비격리 호스트에서 각각 5회인 만큼 개선율을 보편적인 상수나 SLA로 확정하지 않는다.

별도 레코드 microbenchmark(각 5회)의 할당 중앙값은 PLACE 4,901→1,178B/op·136→15 allocs/op, CANCEL 3,536→729B/op·80→11 allocs/op였다. 이 microbenchmark는 CI/fuzz와 동시 실행되어 ns/op 편차가 컸다. 그 시간 값은 개선율 판단에 사용하지 않고, 위 본 캠페인은 이들 종료 뒤 별도로 수행했다. 할당 표본과 원본 전체 로그는 `var/wal-decoder-micro-20261113.log`에 보존했다.

채택 근거는 기존 입력 판정·상태 bytes/복구 계약 회귀 통과와 같은 입력의 복구/할당 감소다. 더 이상 범용 strictjson을 쓰지 않는다고 검사를 생략한 것으로 보지 않으며, canonical 재인코딩과 전체 입력 일치는 필수로 유지한다. 작은 mutable JSON map이나 RawMessage가 포함되는 새로운 record schema가 필요하면 닫힌 DTO 가정을 다시 검증해야 한다.

## 검증·재생성과 한계

- 기존 decoder reference body가 `ec420e0`의 decodeRecord 본문과 완전히 같음을 확인했다. 정상 PLACE/CANCEL·최대 순번/가격/수량/ID, 모든 prefix 절단/byte flip, 외부/내부 필드 중복·누락·case/escape·재순서·null/잘못된 타입·정수/UTF-8·trailing/depth 변형과 잘못된 runId를 차등 검사했다. 결과 record·성공 여부·ErrCorrupt 분류가 같다.
- differential fuzz 30초 설정: 464,584회·불일치 0, reader fuzz 30초 설정: 2,376,063회·실패 0. `var/wal-decoder-differential-fuzz-20261113.log`, `var/wal-decoder-reader-fuzz-20261113.log`와 Go 캐시의 corpus를 유지했다. 유한 fuzz/고정 상태 검증을 모든 입력에 대한 수학적 증명으로 확장하지 않는다.
- 로컬 전체 CI: portable 226개/23 packages, native race 264개/23 packages(기존 명시된 child-helper skip 1개), 각 Python 66개와 진단/저장/HTTP/프로세스·최초 결과/snapshot 복구 smoke 통과. `var/ci/portable.QL3JTa`, `var/ci/native.E2sCo5`; `var/wal-decoder-ci-portable-20261113.log`, `var/wal-decoder-ci-native-20261113.log`.
- Python 4개 새 회귀는 조건별 5회/버전 교대·고정 비교 범위·read-only 집계/쌍별 감소율·누락/오류/잘못된 상태·다른 production/probe 변경 거절을 검사한다. `var/wal-decoder-smoke-20261113`의 8 worker는 작은 실제 동결 빌드 비교 기능 검사다.
- 본 자료 `var/wal-decoder-full-20261113`: 40 worker·383개 봉인 파일, 약 456MiB. 원본 입력 588개 파일과 두 동결 source archive를 다시 확인했다. 이전 실험/실패를 삭제하지 않았으며 4GiB 묶음/8GiB 여유 예산을 지켰다(종료 여유 약 11GiB).
- `var/wal-decoder-reanalysis-20261113`에서 원본으로 새 report.json/report.md를 생성했고 bytes가 같다. `var/wal-decoder-final-proof-20261113.json`에 source archive·현재 candidate source·구형 reference·입력/본 자료 봉인·보고서 일치를 기록했다.

cold-cache·snapshot 이후 큰 WAL tail·다중 계정/가격 단계·새 HTTP 처리량·큰 상태의 장기 운전은 이번 범위 밖이다. 기존 profiler의 CPU 비중을 새로운 코드의 CPU profile 비중이라고 재사용하지 않는다. 실제 OS·전원 장애는 미완료·보류이고 v1 전체 계획은 계속 Blocked다. 위 검증은 구현 완료 시점의 로컬 결과이며 후속 커밋·병합·푸시와 원격 CI는 별도로 확인한다.
