# 스냅샷 배열 할당 비교 도구

`tools/diagnostics/snapshot_array_compare.py`는 동결한 두 생산 빌드의 공개 `runtime.Open`을 같은 snapshot-tail 자료로 비교한다. Python 3.9 이상 표준 라이브러리, 프로젝트 Go wrapper, macOS arm64/APFS/cgo가 필요하며 외부 의존성은 없다.

## 실행

생산 코드를 수정하기 **전에** 기준 빌드를 동결한다. 두 버전에서 Go 측정 probe는 같아야 한다. `var/snapshot-tail-full-20261113`은 기존 [이후 명령 측정](snapshot-tail-recovery.md)의 봉인 자료다. 입력이 없다면 해당 도구로 먼저 생성한다.

```sh
python3 tools/diagnostics/snapshot_array_compare.py freeze --label baseline --output var/array-baseline-new
# internal/snapshot/decode.go의 후보 수정·회귀 검사 후
python3 tools/diagnostics/snapshot_array_compare.py freeze --label candidate --output var/array-candidate-new
python3 tools/diagnostics/snapshot_array_compare.py run \
  --bundle var/snapshot-tail-full-20261113 \
  --baseline var/array-baseline-new --candidate var/array-candidate-new \
  --output var/array-comparison-new
python3 tools/diagnostics/snapshot_array_compare.py analyze \
  --bundle var/array-comparison-new --output var/array-reanalysis-new
```

출력은 기존 parent 아래 새 이름만 허용한다. 짧은 검사에는 `--smoke`와 24명령 smoke 입력 묶음을 함께 사용한다. full은 1만 명령·active/history·이후 명령 0/1천/5천·버전별 5회로 60개, smoke는 24명령·0/8/12·각 1회로 12개 worker다. 조건 순서를 회전·반전하고 각 대응 쌍의 버전 순서를 교대한다. 오래된 시간 수치를 baseline으로 섞지 않는다.

생산 차이는 `internal/snapshot/decode.go` 하나만 허용하고 Go probe·toolchain은 같아야 한다. baseline 생산 코드가 입력 생성 시점과 같아야 한다. source archive의 모든 멤버·내용 hash, 바이너리·묶음 manifest, 현재 source가 candidate 동결본과 같은지 확인한다. freeze baseline은 추적된 internal/cmd/module 변경이 있으면 거절한다. source archive에는 실제 uncommitted 수정도 포함하며 revision만으로 candidate를 재현했다고 주장하지 않는다.

## 계측과 검증

새 프로세스/새 사본의 공개 Open wall·TotalAlloc 차이·load/restore/replay/confirm 시간을 기록한다. 준비·파일 해시·강제 GC·후속 거래 검증·종료 비용은 Open wall 밖이며, 자연 GC와 내부 I/O 대기는 포함한다. CPU/할당 profile·IO observer는 끈다. 읽기 버퍼는 생산 기본 32KiB다.

복구 직후 엔진을 살아 있게 유지한 강제 GC 뒤 `HeapAlloc`을 기록한다. `tail-measure --resident`는 바로 이어 같은 프로세스의 Mach `task_info(MACH_TASK_BASIC_INFO).resident_size`로 **현재 RSS**를 읽는다. 이는 해제되지 않은 Go arena/stack/native 페이지도 포함하며 heap과 같지 않다. compressed memory·swap·physical footprint는 측정하지 않는다. 별도의 `getrusage.ru_maxrss`는 worker 시작부터 Open 직후까지의 **누적 최대 RSS**이며 GC 뒤 현재 RSS와 구분한다. 샘플 시점이 달라 현재 RSS가 앞서 읽은 peak보다 작아야 한다는 조건을 두지 않는다.

두 버전 모두 `GOMAXPROCS=CPU 수`, `GOGC=100`, `GOMEMLIMIT=2GiB`를 사용한다. GOMEMLIMIT는 Go의 soft limit이며 운영 프로세스의 강제 RSS 제한이 아니다. 측정 검증기는 Open peak RSS와 GC 뒤 heap/현재 RSS가 각각 2GiB 이하여야 통과한다. payload 2GiB 제한 자체가 전체 복구 상태 메모리를 보장하지 않는 기존 계약도 유지한다.

정확한 prefix snapshot 선택·재생 명령 수·fallback 없음, 최초 응답 8개/동일 ID 재시도 2개·후속 체결·취소/예약금 반환·독립 잔고와 주문/체결 cursor·최종 canonical snapshot bytes/전체 WAL 불변을 매 worker에서 확인한다. 기존 engine·checkpoint를 통한 복구 검증과 독립 정수 기대값을 사용하며 별도 매칭 엔진과의 대조는 아니다.

## 공간·재집계

8GiB 여유/4GiB 출력 예산과 준비 공간 128MiB를 유지한다. 두 고정 바이너리/source archive, 공통 두 상태의 Genesis/WAL/세 snapshot·응답 receipt, 각 worker의 case/result/stdout/stderr를 보존한다. 성공한 새 소유 scratch만 정확한 파일 집합·소유 marker·SHA/size 검증 후 `verified-files.json`을 남기고 정리한다. 실패/예상 밖 파일은 보존하며 기존 자료는 삭제하지 않는다.

analyze는 봉인·동결 source·60개 결과·공통 fixture·정리 receipt와 메모리/응답 검증을 다시 확인해 JSON/Markdown을 새 폴더에 만든다. 바이너리 재실행이나 원래 외부 입력 경로 접근은 하지 않는다. 실행 당시 사본 검증과 재집계 검증을 구분한다. 성공 사본은 공통 fixture와 고정 빌드로 재구성할 수 있다.

warm-cache·비격리 호스트 관찰로 유휴 gate/cache flush는 없다. 느린 표본을 제외하거나 교체하지 않는다. HTTP p99·최대 지속 처리량 개선과 실제 OS·전원 장애 검증은 범위 밖이며 전원 장애는 **미완료·보류**다. [이번 결과](benchmarks/snapshot-array-allocation.md)를 함께 읽는다.
