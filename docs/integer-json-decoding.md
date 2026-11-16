# 정수 JSON 디코딩 최적화와 같은 상태 비교

[최신 읽기 프로파일](benchmarks/post-optimization-profiling.md)에서 확인한 Int.UnmarshalJSON 임시 문자열 할당을 줄인다. 1~19자리 quoted ASCII digits를 확인한 뒤 ParseInt를 직접 호출하고, 나머지는 기존 JSON parser로 넘긴다. 정수·HTTP·WAL/snapshot 파일의 허용/거절·오류/복구 계약은 그대로다. 실제 결과와 채택 판정은 [비교 보고서](benchmarks/integer-json-decoding.md)에 기록한다.

## 구현과 검증

`internal/domain/integer.go` 하나가 생산 변경이다. 모든 내부 byte가 숫자인지 확인한 뒤 파싱하므로 잘못된 JSON에 overflow가 섞인 오류 우선순위도 유지한다. 공백/이스케이프/길이 초과/invalid UTF-8 등은 이전 경로로 간다. 성공할 때만 receiver에 대입하며 입력을 보관/수정하지 않는다. 정규형 비교나 checksum·WAL prefix 검증을 생략하지 않는다.

독립 legacy oracle로 직접 메서드와 `json.Unmarshal` 경유의 값·정확한 오류 타입/문구·ErrInteger/ErrOverflow·receiver/입력 보존을 대조한다. 경계/혼합 오류 corpus, 고정 seed의 숫자/leading zero/escape/임의 bytes와 fuzz를 사용한다.

```sh
scripts/go test -mod=readonly ./internal/domain ./internal/snapshot ./internal/wal ./internal/strictjson ./internal/api
scripts/go test -mod=readonly ./internal/domain -run '^$' -fuzz '^FuzzIntegerUnmarshalCompatibility$' -fuzztime=30s -parallel=4
scripts/go test -mod=readonly ./internal/domain -run '^$' -bench '^BenchmarkIntegerUnmarshalJSON$' -benchmem -benchtime=200ms -count=5
```

microbenchmark는 레거시/현재의 같은 수신자·입력을 비교한다. 정상 작은 값/최대 값과 escape fallback을 분리하며 엔진 전체 복구 성능으로 확대하지 않는다. 다른 측정/fuzz와 겹치지 않게 실행한다.

## 고정 빌드·복구 비교

macOS arm64/APFS/cgo·프로젝트 Go wrapper/Python 표준 라이브러리만 사용한다. 입력은 봉인된 `canonical_buffer.py` 결과의 candidate 생산 버전과 같은 상태여야 한다. 새 clone에는 var가 없으므로 [원본 패키징/복원](evidence-packages.md)으로 준비한 자료 또는 기존 해당 실험 자료가 필요하다. baseline은 정수 fast path 적용 전 checkout에서, candidate는 적용 후 checkout에서 동결한다. 같은 Go probe/환경을 유지하고 허용 생산 차이는 integer.go 하나다.

```sh
# 생산 코드 변경 전, baseline을 먼저 동결
python3 tools/diagnostics/integer_json_compare.py freeze --label baseline --output var/integer-json-baseline-new
# integer.go 변경·검증 후 candidate 동결
python3 tools/diagnostics/integer_json_compare.py freeze --label candidate --output var/integer-json-candidate-new

# 각 버전 5회·두 상태·세 tail, 총 60개 새 프로세스
python3 tools/diagnostics/integer_json_compare.py run --bundle var/canonical-buffer-full-20261113 --baseline var/integer-json-baseline-new --candidate var/integer-json-candidate-new --output var/integer-json-new

# 작은 24명령·tail0/8/12, 각 버전 한 번·총12 worker
python3 tools/diagnostics/integer_json_compare.py run --smoke --bundle var/canonical-buffer-smoke-20261113 --baseline var/integer-json-baseline-new --candidate var/integer-json-candidate-new --output var/integer-json-smoke-new

# 보존된 원본/빌드/receipt 검증만으로 새 폴더에 JSON·Markdown 재생성
python3 tools/diagnostics/integer_json_compare.py analyze --bundle var/integer-json-new --output var/integer-json-analysis-new
```

run은 원본 봉인/기존 canonical 비교 결과를 검사하고 baseline 생산 source가 입력 candidate와 같은지 확인한다. 새 출력만 허용하고 전체 입력을 수정하지 않는다. source/archive/binary SHA·toolchain·프로브 동일성·현재 candidate source를 확인하며 scratch는 소유권 marker·완료 상태와 전체 파일 해시/크기를 확인한 성공 사본만 receipt를 남기고 제거한다. 실패 사본/원본 fixture/고정 빌드는 보존한다. analyze는 원본 입력 경로 접근이나 보관 바이너리 실행 없이 결과를 검증한다. 기존 배열 비교 runner를 재사용하되 허용 변경 파일과 입력 producer를 명시한다. 원래 배열 실험의 기본 동작/과거 재집계는 유지한다.

두 상태 총1만 명령·이후0/1천/5천 명령 각 버전5회다. 전체 최종 bytes·선택 snapshot/replay 순번·fallback 부재·최초8응답·같은 ID 재시도2건·후속 체결/취소와 예약 반환·WAL 불변을 확인한다. CPU/할당 profiler는 끄고 같은 `tail-measure --resident` probe를 사용한다.

Open wall에는 API 내부 자연 GC/I/O를 포함하며 fixture 준비/사본/추가 강제 GC·후속 검증은 제외한다. TotalAlloc은 공개 Open 전후 정확한 카운터 차이다. heap은 엔진을 살린 GC 뒤 HeapAlloc, 현재 RSS는 Mach resident_size, 누적 peak RSS는 worker 시작부터 Open 직후 high-water다. 서로 다른 메모리 계수이며 같은 값이 될 필요가 없다.

기존 GOGC100/GOMEMLIMIT2GiB·최대 관측 메모리2GiB·4GiB 출력 상한·별도8GiB 디스크 여유·준비128MiB gate를 유지한다. warm-cache·CPU 유휴 gate 없는 비격리 호스트이며 느린 표본을 포함한다. JSON/Markdown에는 중앙값·최소/최대·개별 쌍 변화를 남긴다. HTTP p99·지속 처리량·누수 부재·cold-cache를 증명하지 않는다. 실제 전원 장애는 **미완료·보류**다.
