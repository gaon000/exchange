# 스냅샷 항목 직렬화 전후 비교

`snapshot_leaf_compare.py`는 동일 상태 새 프로세스에서 Snapshot 생성의 시간·할당과 두 Open 복구 경로를 비교한다. 기존 [WAL 비교 조정기](wal-decoder-comparison.md)의 동결/사본/예산/원본 검증을 재사용하지만, 생산 차이를 `internal/snapshot/codec.go` 하나로 제한하고 생성 지표를 먼저 표시한다. 기존 WAL CLI의 기본 허용 경로와 JSON/Markdown은 유지한다.

```sh
# 스냅샷 코드를 수정하기 전 baseline 동결
python3 tools/diagnostics/snapshot_leaf_compare.py freeze --label baseline --output var/snapshot-leaf-baseline-new

# 변경·회귀 검증 뒤 candidate 동결
python3 tools/diagnostics/snapshot_leaf_compare.py freeze --label candidate --output var/snapshot-leaf-candidate-new

# 두 상태/두 복구 경로/두 버전/각 5회, 40 worker
python3 tools/diagnostics/snapshot_leaf_compare.py run --bundle var/state-scale-full-20261113 --baseline var/snapshot-leaf-baseline-new --candidate var/snapshot-leaf-candidate-new --output var/snapshot-leaf-full-new

# 원본 봉인 검증 후 새 폴더에 같은 보고서 생성
python3 tools/diagnostics/snapshot_leaf_compare.py analyze --bundle var/snapshot-leaf-full-new --output var/snapshot-leaf-reanalysis-new
```

macOS arm64/APFS/cgo·정확한 Go wrapper·Python 표준 라이브러리, 장비 sysctl 읽기를 사용한다. 외부 dependency는 추가하지 않는다. baseline 동결은 production 추적 변경을 거절하며, 두 동결 빌드의 생산 Go/Go 설정 차이는 snapshot codec 한 파일이어야 한다. 측정 `tools/statescale` source와 toolchain은 동일해야 한다. 이미 변경한 현재 코드를 과거 baseline으로 표시하지 않는다.

기능 smoke는 `run --smoke --bundle <12명령 상태 비용 smoke 자료>`로 각 조건 1회/총 8 worker다. 본 수치는 각 버전·각 상태 5회의 첫 새 Snapshot이며 WAL 전체 복구 뒤에 측정한다. snapshot 복구 뒤 같은 파일 재확인 publication은 새 생성 통계에 포함하지 않는다. 두 복구 경로의 Open도 각각 5회 기록하고 전체 canonical snapshot·WAL/Genesis/lock bytes가 같은지 확인한다.

각 worker는 같은 fixture의 새 사본과 새 프로세스다. 반복별 버전/조건 순서를 교대한다. CPU·할당 profile은 켜지 않는다. GOMAXPROCS=호스트 논리 CPU 수/GOGC=100/GOMEMLIMIT=2GiB, warm-cache·tail 0·비격리 호스트 조건이다. 사본/해시/강제 GC/후속 검증은 공개 API wall 밖이며 자연 GC·I/O 대기는 안에 포함된다. 강한 sync/전체 WAL prefix 검사·snapshot 확인은 생략하지 않는다.

출력 parent가 있어야 하며 기존 출력/입력 안/symlink를 재사용하지 않는다. 준비 공간 128MiB와 별도 디스크 여유 8GiB, 실제 fixture/worker 복사량 및 전체 4GiB 예산을 검사한다. 현재 checkout의 코드와 바이너리를 실행 중 다시 대조한다. 원본/실패/느린 표본은 삭제·대체하지 않는다. `builds`/`fixtures`/`runs`와 report.json/report.md·manifest를 보존하며 analyze는 새 폴더에서만 보고서를 생성한다.

새 clone에는 `var`가 없다. [상태 비용 도구](state-size-costs.md)로 full 자료를 새로 만들거나 보존한 입력이 필요하다. 2026-11-13 과거 수치 재집계에는 해당 `var/snapshot-leaf-full-20261113`의 동결 빌드/자료가 필요하다. baseline production 기준은 WAL 최적화를 포함한 `dc8635f`다.

[실제 비교](benchmarks/snapshot-leaf-serialization.md), [JSON](benchmarks/20261113-snapshot-leaf-comparison.json)을 따른다. 고정 상태의 할당·시간 관찰을 HTTP p99/최대 지속 처리량 개선으로 확대하지 않는다. 실제 전원 장애 검증은 미완료·보류다.
