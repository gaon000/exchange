# 복구 뒤 RSS 증가 진단 도구

`tools/diagnostics/recovery_rss.py`는 정규형 비교 버퍼 최적화의 두 생산 decoder를 같은 확장 메모리 probe에서 실행한다. 이력형/tail=0의 RSS 증가가 반복되는지와 시간·추가 GC·OS 반환 단계의 영향을 확인한다. 활성 주문형/tail=0을 대조로 함께 사용한다. [결과](benchmarks/recovery-rss.md)를 함께 읽는다.

## 명령

macOS arm64/APFS/cgo·Python 3 표준 라이브러리·프로젝트 Go wrapper가 필요하다. 현재 생산 코드가 입력 묶음의 candidate와 같아야 한다. 수정하는 경로는 tools/문서이며 생산 코드를 바꾸지 않는다.

```sh
# 원본을 보존하며 두 생산 버전·같은 probe/상태로 총 20개 새 worker
python3 tools/diagnostics/recovery_rss.py run \
  --bundle var/canonical-buffer-full-20261113 \
  --output var/recovery-rss-new

# 작은 실제 복구/계측 확인: 두 상태 × 두 버전 = 4 worker
python3 tools/diagnostics/recovery_rss.py run --smoke \
  --bundle var/canonical-buffer-smoke-20261113 \
  --output var/recovery-rss-smoke-new

# 원 입력 경로/바이너리 실행 없이 새 폴더에 JSON/Markdown 재집계
python3 tools/diagnostics/recovery_rss.py analyze \
  --bundle var/recovery-rss-new --output var/recovery-rss-analysis-new
```

새 clone에는 var가 없으므로 기존 묶음을 복원하거나 앞선 실험 도구로 원본을 준비해야 한다. 입력의 봉인·기존 profile/60회 결과·고정 빌드/source archive를 검증하고 final snapshot/전체 WAL/Genesis/lock 및 선택한 이전 결과를 새 묶음에 복사한다. 기준/후보 decoder는 입력의 두 archive에서 읽어 SHA를 확인한다.

최신 Go probe는 두 빌드에 똑같이 넣고 `internal/snapshot/decode.go` 하나만 Go build overlay로 바꾼다. 두 source archive에는 실제로 컴파일한 decoder bytes를 넣으며 모든 Go probe source는 같아야 한다. 실제 모든 internal/cmd 생산 source·Go module/version이 원래 해당 버전과 같아야 한다. overlay mapping·원 bytes/해시·바이너리·Go/환경·revision/dirty 정보를 보존한다. 이전 고정 바이너리 자체를 최신 probe로 계측한 것은 아니다.

## 관측 순서

full은 같은 총 1만 명령·tail=0(최종 snapshot에서 복구)에서 active/history × 두 버전 × 5회다. 반복마다 상태 순서를 뒤집고 각 상태에서 버전 순서를 교대한다. 성공/느린 관측을 골라 교체하지 않는다. 하나의 full worker는 약17초 이상의 진단 관측을 하며 전체 실행은 수 분 걸린다.

| 단계 | 목표 관측 시각 | 목적 |
| --- | --- | --- |
| before-open | Open 전 GC 뒤 1회 | 준비 후 시작 상태 |
| after-open | Open 직후·추가 GC 전 1회 | 일시 객체를 포함한 복구 직후 |
| idle | 기존 복구 뒤 GC 후 0/100ms/1/5/15초 | 새 거래·조회 없이 자연 유휴 관측 |
| gc-only | 추가 runtime.GC 후 0/100ms/1초 | 추가 GC의 영향 분리 |
| force-release | debug.FreeOSMemory 후 0/100ms/1초 | GC+OS 반환 시도 진단 |

smoke는 idle 0/1/5/10/20ms와 각 control 0/1/5ms다. 13개 표본마다 목표/실제 경과·수집 소요 시간을 기록한다. sleep/스케줄링 지연을 숨기지 않는다. MemStats→현재 RSS→누적 peak→VM 계수를 순서대로 읽으므로 하나의 원자적 snapshot이 아니다.

FreeOSMemory에도 GC가 포함되므로 GC-only 단계를 먼저 둔다. 강제 반환을 하지 않은 별도 프로세스와의 무작위 대조가 아니며 단계 순서 효과가 남는다. 이 API는 진단 자식 프로세스에만 호출하고 거래소/운영 설정에는 추가하지 않는다. [공식 설명](https://pkg.go.dev/runtime/debug#FreeOSMemory)을 참고한다.

관측 중 Runtime/엔진을 살아 있게 유지한다. 모든 최초 응답·같은 ID 재시도·거래/잔고 조회·검증용 Snapshot·종료는 관측이 끝난 뒤 수행한다. 복구 경로(snapshot seq=최종/재생0/fallback 없음), 최초 응답8개/재시도2개·후속 체결/취소/예약 반환/잔고·최종 canonical 파일/전체 WAL 불변 검증은 기존 tail probe와 같다. 기존 engine/checkpoint 복구 oracle과 독립 정수 기대값이며 독립 매칭 엔진과의 비교는 아니다.

기존 tail-measure/default profile 경로는 관찰자가 nil이며 시간별 관측이나 추가 GC/강제 반환을 실행하지 않는다. 새 RSS 진단의 Open 시간/할당은 관찰자가 있는 값이므로 이전 비계측 성능과 혼합하지 않는다.

## 계수 해석

- `RSSBytes`: Mach MACH_TASK_BASIC_INFO의 현재 resident_size. `PeakRSSBytes`: getrusage 생애 누적 최대이며 GC 뒤 현재 값과 구분한다.
- `VM`: TASK_VM_INFO의 resident_size/phys_footprint/reusable/compressed/internal/external/virtual_size. SDK가 요구하는 revision을 제공하지 못하면 실패한다. 각 계수는 다른 범위이며 단순히 더해 RSS를 만들지 않는다.
- `Memory`: HeapAlloc/Inuse/Sys/Idle/Released/Objects·StackInuse/Sys·span/cache/GC/other/sys·할당/해제/GC count를 보존한다. HeapSys는 가상 heap 주소 공간이며 현재 RSS가 아니다.
- `retainedIdleMiB = HeapIdle - HeapReleased`: 아직 runtime이 보유한 idle heap 추정치. `unusedInuseMiB = HeapInuse - HeapAlloc`: in-use span의 미사용 공간 상한. 둘 다 RSS의 정확한 분해가 아니다.
- `goSysLessReleasedMiB = Sys - HeapReleased`: Go 관리 메모리의 별도 지표이며 프로세스 전체 RSS/physical footprint와 같지 않다.

[Go MemStats](https://pkg.go.dev/runtime#MemStats), [Darwin의 Go 반환 경로](https://go.dev/src/runtime/mem_darwin.go)와 실제 설치한 Go 1.27.3 source/로컬 macOS SDK의 task_info 구조 정의를 함께 확인했다. Go의 Darwin 반환은 MADV_FREE_REUSABLE 경로를 쓰며 task_info 회계 갱신과 관련이 있다. 이 설명만으로 각 표본의 모든 RSS 차이를 단일 원인에 귀속하지 않는다.

## 보존·검증·한계

GOMAXPROCS=CPU 수/GOGC=100/GOMEMLIMIT=2GiB, profiler/trace/GODEBUG를 고정/해제한다. GOMEMLIMIT는 Go soft limit이고 sample의 heap·현재/최대 RSS·VM resident/footprint가 각각 2GiB 이하여야 통과한다. 생산의 값/배열/payload·저장/복구 상한도 유지한다.

생성 전 128MiB 준비 여유, 매 복사/빌드/실행 전 별도 8GiB 여유·4GiB 묶음 예산을 검사한다. 성공 소유 scratch만 정확한 marker/파일 집합/모든 SHA+size 확인 후 receipt를 남기고 정리한다. 기존 원본/고정 빌드/실패/예상 밖 파일은 보존한다. 새 command는 실패해도 가능한 부분 표본과 오류를 새 result에 남긴다.

analyze는 전체 봉인·원본 SHA·두 archive/overlay/Go probe 동일성·모든 결과/정리 receipt·실제 시각·메모리 관계·추가 GC/반환 단계와 복구 보장을 재검증한다. 이전 원본에서 복사한 20개 표본과 새로운 진단을 구분해 JSON/Markdown을 만든다. 다른 빌드/observer의 값을 한 집단으로 합치지 않는다.

비격리 개발 호스트·warm-cache·새 프로세스의 제한된 상태/시간 관측이다. 장기간 요청/반복 Open을 하나의 살아 있는 프로세스에서 수행한 누수 검증이나 모든 환경의 RSS 보장은 아니다. CPU/할당 profile·10분 부하/HTTP p99·최대 지속 처리량·실제 OS/전원 장애는 이번 범위 밖이다. 전원 장애는 **미완료·보류**다.
