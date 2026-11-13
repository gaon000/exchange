# WAL 디코더 고정 상태 비교

`tools/diagnostics/wal_decoder_compare.py`는 변경 전후의 `tools/statescale` probe를 소스·바이너리와 함께 동결하고, 같은 fixture의 새 사본/프로세스에서 공개 Open 비용과 완전한 복구 일치를 비교한다. CPU/메모리 profiler를 켜지 않는다. Python/Go 표준 도구만 사용하며 환경은 [개발 기준](development.md)을 따른다.

## 빌드 동결과 실행

```sh
# production 코드를 수정하기 전 baseline 동결
python3 tools/diagnostics/wal_decoder_compare.py freeze --label baseline --output var/wal-decoder-baseline-new

# WAL 디코더 변경·회귀 검사 후 candidate 동결
python3 tools/diagnostics/wal_decoder_compare.py freeze --label candidate --output var/wal-decoder-candidate-new

# 두 상태 × 두 복구 경로 × 이전/후보 각 5회 = 40 worker
python3 tools/diagnostics/wal_decoder_compare.py run --bundle var/state-scale-full-20261113 --baseline var/wal-decoder-baseline-new --candidate var/wal-decoder-candidate-new --output var/wal-decoder-full-new

# 원본 전체 검증 후 새 폴더에 JSON/Markdown 재생성
python3 tools/diagnostics/wal_decoder_compare.py analyze --bundle var/wal-decoder-full-new --output var/wal-decoder-reanalysis-new
```

baseline 동결은 HEAD 대비 `internal`/`cmd`/Go 설정의 추적된 변경이 있으면 거절한다. 측정하는 생산 `.go`(테스트 제외)·Go 설정의 두 버전 간 차이는 `internal/wal/codec.go` 한 파일이어야 하고, `tools/statescale` probe source와 toolchain은 같아야 한다. label/hash·전체 봉인·source archive를 보존하며 실행마다 같은 동결 바이너리만 사용한다. 이미 최적화한 HEAD를 과거 baseline이라고 표시해 비교하지 않는다.

새 clone에는 `var`가 없다. [상태 비용 도구](state-size-costs.md)로 새 full fixture를 준비하거나 보존한 자료가 필요하다. 2026-11-13의 과거 수치 자체를 재생성하려면 당시 비교 묶음의 동결 빌드와 입력이 필요하다. 과거 baseline source 기준은 `ec420e0`이며, 현재 코드를 빌드한 것만으로 그 이전 바이너리를 재현했다고 하지 않는다.

작은 기능 검사는 같은 두 동결 빌드에 `--smoke --bundle <12명령 상태 비용 smoke 자료>`를 사용한다. 조건별 1회·8 worker이며 full 성능 결과에 섞지 않는다. 출력 parent는 존재해야 하며 기존 폴더/입력 안/심볼릭 링크를 재사용하지 않는다. macOS arm64/APFS/cgo·sysctl 장비 읽기 권한이 필요하다.

## 조건과 판정

- 10,000명령 active/history 각각에서 snapshot 없는 WAL 전체 복구와 최종 snapshot+전체 WAL 복구를 측정한다. 후자의 tail=0이다. 기존 prefix 입력/순번 대조와 strict sync를 유지한다.
- 각 조건은 baseline/candidate 각각 5회다. 홀수 반복은 baseline→candidate, 짝수는 반대 순서이며 조건 순서도 뒤집는다. 20쌍 전체를 보존하고 느린 표본도 제외하지 않는다.
- worker는 새 사본/새 프로세스다. GOMAXPROCS=호스트 논리 CPU 수/GOGC=100/GOMEMLIMIT=2GiB를 고정한다. 사본/해시 때문에 warm-cache이며 cache flush·유휴 gate로 격리한 캠페인은 아니다. 전체 CI·fuzz·microbenchmark를 끝낸 뒤 본 측정을 실행했다.
- 공개 `runtime.Open` wall/TotalAlloc/GC 뒤 heap·누적 RSS와 recovery stage를 기록한다. 준비/사본/해시/강제 GC/후속 검증·shutdown은 wall 시간 밖이며, 공개 호출 안의 자연 GC·I/O 대기는 포함된다.
- WAL 경로 뒤 첫 새 Snapshot 생성 시간/할당도 대조한다. snapshot 경로 뒤 같은 파일 재확인은 새 생성 통계와 혼합하지 않는다. `state_scale`의 계측·상태 검증을 재사용한다.
- 모든 worker의 recovery 선택/적용 수·fallback 부재·전체 canonical snapshot SHA-256과 WAL/Genesis/lock 불변을 확인한다. engine/order/FIFO/자금/체결/최초 요청·결과 bytes가 포함된다.
- 출력 생성/빌드 전 8GiB+준비 공간 128MiB, 각 복사 전 실제 크기 예산·전체 자료 4GiB 상한을 검사한다. 부족할 때 원본을 지우지 않는다. 공개 API/자식 프로세스 timeout과 Go 메모리 soft limit은 상태 비용 probe와 같다.

`builds`는 양쪽 동결 source/binary/환경/봉인, `fixtures`는 사용한 고정 상태, `runs/NNN`은 순서/버전·원본 JSON·로그·run 사본이다. report는 조건별 각 5회의 중앙값·최소/최대, 중앙값 변화율·20개 개별 쌍 변화를 보존한다. 할당량은 유지 heap과 다른 누적 임시 할당을 포함한다. stage 중앙값을 합쳐 전체 Open 중앙값으로 쓰지 않는다.

원본/다른 크기의 과거 latency와 섞지 않으며 HTTP p99·최대 처리량·무제한 장기 운전의 개선을 주장하지 않는다. 실제 OS·전원 장애 검증은 **미완료·보류**다. [실제 5쌍 결과](benchmarks/wal-decoder-optimization.md)와 [기계 판독 JSON](benchmarks/20261113-wal-decoder-comparison.json)을 따른다.
