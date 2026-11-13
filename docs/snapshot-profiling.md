# 스냅샷 CPU·할당 프로파일링

`tools/statescale profile`은 고정 fixture 사본 하나에서 `runtime.Open` 또는 새 `runtime.Snapshot`만 계측한다. `snapshot_profile.py`는 기존 상태 비용 자료를 검증하고 필요한 fixture를 복사해 새 프로세스에서 실행·집계한다. production 거래/저장/복구 계약과 의존성은 변경하지 않는다.

```sh
# 기존 12명령 smoke 자료의 같은 상태로 기능 확인
python3 tools/diagnostics/snapshot_profile.py run --smoke --bundle var/state-scale-smoke-20261113 --output var/snapshot-profile-smoke-new

# 기존 full 자료의 active/history 각 1만 명령 상태
python3 tools/diagnostics/snapshot_profile.py run --bundle var/state-scale-full-20261113 --output var/snapshot-profile-new

# 봉인된 profile 자료만으로 새 JSON/Markdown과 pprof 보고서 생성
python3 tools/diagnostics/snapshot_profile.py analyze --bundle var/snapshot-profile-new --output var/snapshot-profile-analysis-new
```

입력은 [상태 비용 도구](state-size-costs.md)가 생성한 완전한 봉인 자료다. 새 clone에는 로컬 `var` 원본이 없으므로 먼저 상태 비용 명령으로 자료를 준비하고 `--bundle`을 그 경로로 지정한다. macOS arm64/APFS/cgo·정확한 Go wrapper와 Python 표준 라이브러리만 사용한다. `sysctl` 장비 정보 읽기가 필요하다. 출력 parent가 존재해야 하며 기존 출력/입력 자료 안/심볼릭 링크는 거절한다.

## 경로와 표본

- `wal-open`: snapshot이 없는 사본의 전체 WAL 복구. 기준 대조 경로다.
- `snapshot-open`: 최종 snapshot과 전체 WAL이 있는 사본의 공개 Open. tail=0이며 prefix 검증·snapshot 재확인/sync를 포함한다.
- `create`: snapshot 없는 사본을 먼저 정상 Open한 뒤 첫 새 snapshot을 생성한다. 준비 Open은 해당 프로파일 밖이다.

두 상태마다 CPU는 Open 경로 각 20회·create 40회, 할당은 각 경로 5회로 총 190개 새 worker를 사용한다. CPU와 할당은 별도 프로세스다. smoke는 각 한 번의 12 worker다. 반복 순서/상태 순서는 교대한다. 모든 worker의 Genesis/WAL/lock과 생성한 전체 canonical snapshot SHA-256이 기준과 같아야 한다. 복구 report의 선택 순번·tail 적용 수·fallback 부재도 확인한다.

입력/사본/해시는 계측 밖이며 cache flush를 하지 않는 warm-cache 조건이다. snapshot 생성 뒤나 복구 뒤의 추가 validation snapshot·SHA·shutdown은 프로파일 밖이다. worker 사본·profile·실패/로그를 보존하고 덮어쓰거나 기존 자료를 삭제하지 않는다. 출력 생성/빌드 전에 8GiB 여유와 준비 공간 128MiB를 검사하고, 각 fixture 복사 전 실제 파일 크기로 예산을 다시 확인한다. 디스크 여유 8GiB/묶음 4GiB 예산과 공개 API timeout을 유지한다.

## CPU와 allocation 해석

CPU는 표준 `runtime/pprof.StartCPUProfile`과 기본 sample period를 사용한다. 공개 호출 전후 counter 읽기와 profile 관리의 부수 표본이 전체 표에 남을 수 있어 internal focus와 구분한다. 프로파일은 wall 지연·sync 대기 시간이 아니라 CPU 표본이다. `raw.txt`의 period와 총 sample 수를 같이 보존한다. 짧은 create는 표본 수가 상대적으로 적고 함수별 순위는 탐색적 근거다. 본 결과는 비계측 기존 latency 수치/처리량 개선과 혼합하지 않는다.

allocation worker는 시작 때 `runtime.MemProfileRate=65536`으로 설정한다. 호출 전후 두 GC 뒤 allocs profile을 기록하고 `go tool pprof -base`로 차이를 만든다. baseline 이후 profile 직렬화/후속 GC·런타임의 부수 비용도 전역 차이에 남을 수 있어 `internal/` 호출 경로를 focus한 표와 전체 표를 함께 보존한다. `alloc_space`와 `alloc_objects`는 샘플링 추정치다. 원본 JSON의 API 전후 TotalAlloc/Mallocs는 별도의 정확한 런타임 counter 차이이며 같은 측정량으로 간주하지 않는다. 이미 해제된 임시 할당도 alloc_space에 포함되므로 유지 heap/RSS가 아니다.

`flat`은 함수 자체, `cumulative`는 하위 호출을 포함한다. 상위/하위 cumulative는 중첩되어 합하지 않는다. internal focus도 내부 함수가 호출한 표준 JSON·allocator 등의 표본을 포함하며 전체 CPU의 GC 백그라운드 비용과 분리해 본다. CPU worker는 기본 메모리 샘플링 512KiB를 사용하고 이를 결과에서 확인한다.

[Go pprof 문서](https://pkg.go.dev/runtime/pprof)와 [runtime memory profile](https://pkg.go.dev/runtime#MemProfile)의 sample/GC/할당 프로파일 의미를 따른다. 실행은 저장소의 정확한 toolchain으로 한다.

## 보존과 재검증

`protocol.json`은 입력 봉인 SHA·순서·환경을, `build.json`/`source.tar.gz`는 새 probe·전체 source/binary hash·Go/장비·dirty 상태를 남긴다. 기존 비용 측정과 `internal/`, `cmd/`, Go 버전/모듈 bytes가 같은지 검사한다. 소스와 동결 바이너리를 매 worker 뒤 다시 확인한다.

`fixtures`는 입력에서 복사한 상태, `runs/NNN`은 조건·실행 로그·사본·개별 CPU 또는 전후 allocs profile/결과다. `analysis`의 12개 그룹은 합친 protobuf, 원본 pprof text, 전체/focus flat·cumulative 표와 할당 객체 수 표를 보존한다. `report.json`/`report.md`의 호출당 값은 그룹 합계를 반복 수로 나눈 값이며 중앙값이 아니다. 원본 개별 profile은 삭제하지 않는다.

재집계는 새 폴더에서만 수행하고 봉인·입력·환경·경로·반복 수를 검증한다. 원본 probe 바이너리를 실행하지 않고 현재 Go tool pprof로 보존 profile을 읽는다. 실패/표본 부족은 좋은 표본만 남겨 성공으로 바꾸지 않는다. 실제 OS·전원 장애 검증은 계속 **미완료·보류**다.

2026-11-13의 실제 190 worker 결과·병목과 후속 후보는 [분석 보고서](benchmarks/snapshot-profiling.md), [JSON 근거](benchmarks/20261113-snapshot-profiling.json)에 보존한다.
