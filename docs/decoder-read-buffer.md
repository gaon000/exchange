# 디코더 읽기 버퍼 대조 실험

`snapshot-tail`의 같은 active/history 각 1만 명령 상태에서 디코더 읽기 버퍼 32/64/128/256KiB, 이후 명령 0/1천/5천을 각 5회·총 120개 새 프로세스로 비교한다. 생산 기본값은 32KiB로 유지하며 checksum/쓰기/confirm의 32KiB 버퍼와 파일·검증·복구 계약을 바꾸지 않는다. [실제 결과](benchmarks/decoder-read-buffer.md)를 함께 읽는다.

## 실행

macOS arm64/APFS/cgo·프로젝트 Go wrapper·Python 표준 라이브러리만 사용한다. 외부 dependency는 없다. 저장소 루트에서 실행한다.

```sh
# 현재 생산 코드가 HEAD와 같은지 확인하고 네 실험 빌드를 동결
python3 tools/diagnostics/read_buffer.py freeze --output var/read-buffer-builds-new

# 전체: 네 버퍼 × 두 상태 × 세 tail × 각 5회
python3 tools/diagnostics/read_buffer.py run --bundle var/snapshot-tail-full-20261113 --builds var/read-buffer-builds-new --output var/read-buffer-full-new

# 기능: 24명령·tail 0/8/12, 조건별 1회·총 24개 worker
python3 tools/diagnostics/read_buffer.py run --smoke --bundle var/snapshot-tail-review-smoke-20261113 --builds var/read-buffer-builds-new --output var/read-buffer-smoke-new

# 원본 검증 후 새 폴더에 같은 JSON/Markdown 재생성
python3 tools/diagnostics/read_buffer.py analyze --bundle var/read-buffer-full-new --output var/read-buffer-analysis-new
```

새 clone에는 `var`가 없다. [tail 도구](snapshot-tail-recovery.md)로 full/smoke 자료를 새로 만들거나 보존한 원본을 사용한다. 이전 측정과 현재 생산 source 집합/내용이 같아야 하고 미추적/ignore 생산 파일도 거절한다. freeze 이후 조정기/테스트를 포함한 source가 바뀌면 새로 동결한다.

## 변형 범위와 회귀

`go build -overlay`로 `internal/snapshot/decode.go`의 `bufio.NewReaderSize(r, bufferBytes)` 인자 한 곳만 숫자 상수로 대체한다. 추적 중인 생산 파일은 바꾸지 않는다. Go 소스 archive·허용 변형 텍스트·overlay mapping·네 바이너리 SHA를 묶고 실행 중 source/binary drift를 검사한다. 32KiB도 같은 방식으로 동결하고 네 값/조건 순서를 반복마다 회전·교대한다.

기존 large FIFO 테스트의 boundedSource는 구현 당시 32KiB read 요청만 허용한다. 실험용 테스트 overlay는 해당 **읽기 상한만** 각 buffer 크기로 바꾼다. boundedSink의 쓰기 상한 32KiB·leaf 값 상한 16KiB·깊이/array streaming·corruption/golden/변경 reader·IO 오류 검사 내용은 그대로다. 테스트 파일은 생산 바이너리에는 포함되지 않는다. overlay 검사기는 이 두 파일의 두 제한된 치환 외 변경을 거절한다.

같은 overlay로 계약 회귀를 실행하려면 다음을 사용한다. `64`를 각 크기로 바꾸고, 먼저 테스트 출력 디렉터리를 새로 준비한다.

```sh
scripts/go test -race -mod=readonly -overlay "$PWD/var/read-buffer-builds-new/64/overlay.json" -count=1 -timeout=180s ./internal/snapshot ./internal/runtime
```

## 시간과 호출 수의 분리

primary 비용은 관찰자 없는 공개 `runtime.Open`의 wall·TotalAlloc·GC 뒤 heap·누적 peak RSS와 recovery stage다. CPU/profile은 켜지 않는다. 기존 tail probe로 복구 뒤 최초 응답 8개/중복 재시도 2개·후속 체결/취소·독립 잔고/예약금 반환과 전체 canonical 최종 snapshot·WAL 불변을 확인한다. 정확한 선택 snapshot seq/재생 수·fallback 부재를 요구한다.

Open과 후속 검증/종료가 끝난 뒤 같은 선택 **prefix snapshot**을 공개 `snapshot.Read`로 두 번 읽는다. 첫 진단은 일반 `os.File.ReadAt` observer, 두 번째는 `syscall.Pread`를 호출하는 ReaderAt adapter다. 두 문서/ReaderAt 요청 패턴이 같고 원본 bytes가 불변이어야 한다. 계수기는 Read 내부의 header/checksum/digest/decode 단계별 요청 횟수·길이 histogram·요청/반환 bytes, 직접 Pread 호출/전달 bytes·EINTR 재시도·short read를 저장한다. 준비용 header 읽기·파일 hash/Stat은 해당 계수 창 밖이다.

직접 Pread 횟수는 **별도 진단의 syscall.Pread 진입 횟수**이며 Open 프로세스의 커널 trace가 아니다. 일반 File.ReadAt 내부의 EINTR 재시도나 OS의 syscall 재시작 횟수를 추정하지 않는다. 오류/부분 읽기는 adapter에서 전파/기록한다. 실제 full 파일의 반환 bytes와 요청 histogram으로 버퍼 label이 실제 decode 읽기 패턴과 맞는지 검증한다.

payload 길이를 P라고 하면 정상 진단의 checksum 요청은 `ceil((P+20)/32768)`, decode 요청은 `ceil(P/buffer)`다. header 20bytes/digest 32bytes 요청이 각각 1회이고 반환 bytes 합은 `2P+72`다. 이는 snapshot Read의 두 checksum을 유지한 결과다. full의 모든 반복에서 검사하며 checksum 요청 수는 같은 cut의 네 버퍼에서 같아야 한다. 직접 Pread 수는 EINTR/short read가 없을 때 ReaderAt 요청 합과 같다.

시간/메모리에는 이 진단과 사본·해시·강제 GC·검증용 Snapshot·종료 비용을 넣지 않는다. 자연 GC·Open 내부 I/O 대기는 포함한다. RSS는 worker 시작 후 Open 직후까지의 누적 high-water이며 API 구간만의 독립 peak가 아니다.

## 공간과 자료 보존

출력은 기존 parent 아래 새 디렉터리만 허용한다. 생성 전 128MiB 준비 공간, 복사/실행 전 8GiB 별도 여유·4GiB 실험 묶음 예산을 검사한다. 실험 묶음에는 네 동결 빌드·source archive/overlay, 두 공통 fixture의 Genesis/WAL/세 snapshot·최초 응답, 각 case/result/io/stdout/stderr를 보존한다.

성공한 worker의 새 임시 `run` 사본은 검증된 전체 파일 SHA/size와 소유 row를 `verified-files.json`으로 저장한 다음에만 정리한다. 삭제 직전 소유 marker·정확한 파일 집합·모든 hash를 확인한다. 예상 밖 파일/변조/symlink/오류가 있으면 중단하고 사본을 남긴다. 기존 자료·공통 fixture·고정 빌드·실패 사본은 삭제하지 않는다. 성공 사본은 공통 fixture의 선택 snapshot/전체 WAL에서 재구성할 수 있으나 실행 당시 사본 자체를 장기 보관한 것은 아니다.

analyze는 봉인·네 빌드·공통 fixture·120개 결과와 hash receipt/호출 histogram을 검증하고 새 보고서만 만든다. 바이너리를 실행하거나 원래 외부 입력 경로를 읽지 않는다. 성공 사본을 재실행해 상태를 독립 재검증하는 작업은 별도이며, 처음 실행 때 public Open/최종 Snapshot 검증이 성공한 기록과 구분한다.

warm-cache·비격리 개발 호스트 관찰이며 cache flush/CPU 유휴 gate를 쓰지 않는다. 같은 시점의 32KiB 결과를 baseline으로 쓰고 과거 다른 빌드/명령열의 수치와 직접 섞지 않는다. 최적 버퍼나 디스크 지연 원인을 한 번의 비교로 확정하지 않는다. 실제 OS·전원 장애/HTTP p99·최대 처리량 검증은 포함하지 않으며 전원 장애는 **미완료·보류**다.
