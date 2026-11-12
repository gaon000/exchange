# 재현 가능한 실험 도구

최근 지연 진단에 사용한 실행·전체 복구·원본 재집계·trace·OS I/O·타이머 도구를 [tools/diagnostics](../tools/diagnostics/experiment.py)에 포함했다. 예전 `var`의 스크립트나 바이너리 없이 현재 checkout에서 실행한다. 입력·환경·실제 명령·source/binary SHA-256을 남겨 같은 조건을 다시 실행하는 절차이며, 호스트 상태나 지연 수치까지 같아진다는 의미는 아니다.

## 준비

저장소 루트에서 실행한다. [개발 환경](development.md)에 따라 정확한 Go 1.27.3을 준비하고 `python3 --version`으로 Python 3.9 이상을 확인한다. Python 표준 라이브러리만 사용하며 pip 설치는 없다.

```sh
sh scripts/setup-go.sh
scripts/go version
python3 --version
python3 tools/diagnostics/experiment.py --help
```

거래를 포함하는 `soak`, `trace`, `io`는 지원 환경인 macOS arm64·로컬 APFS·cgo와 loopback, `ps`, `sysctl` 읽기 권한이 필요하다. `trace`/`io`는 `iostat`, `io`는 Command Line Tools의 clang·libproc도 사용한다. `timer`와 오프라인 재집계는 일반 Go 실행 환경에서도 사용할 수 있다. 커널 내부 추적이나 sudo는 사용하지 않는다.

실행은 최소 여유 디스크 8 GiB를 요구하며 실행 묶음 8 GiB·관찰 대상 RSS 2 GiB 상한을 검사한다. trace는 거래 프로세스마다 512 MiB, 타이머는 128 MiB로 제한한다. 초과·관찰 오류·중단은 실패이며 자료를 보존한다. 경로에는 매번 **새 이름**을 사용한다. 빈 디렉터리도 재사용하지 않는다.

## 먼저 짧게 확인하기

기본 preset은 `smoke`다. 다음 명령은 타이머 네 조건을 각 1초 실행하고, 무결성과 표본의 의미를 따로 검사한다.

```sh
python3 tools/diagnostics/experiment.py run --suite timer --output var/diagnostics/timer-smoke
python3 tools/diagnostics/experiment.py verify --bundle var/diagnostics/timer-smoke --output var/diagnostics/timer-smoke-verified.json
python3 tools/diagnostics/experiment.py analyze --bundle var/diagnostics/timer-smoke --output var/diagnostics/timer-smoke-analysis.json
```

거래·스냅샷·정상 재시작·전체 복구는 다음으로 확인한다. 각 거래 조건은 20 requests/s·warmup 없이 6초이며 준비 주문은 별도다.

```sh
python3 tools/diagnostics/experiment.py run --suite soak --output var/diagnostics/soak-smoke
python3 tools/diagnostics/experiment.py analyze --bundle var/diagnostics/soak-smoke --output var/diagnostics/soak-smoke-analysis.json
```

`trace`는 snapshot 끔/켬에서 Go trace와 호스트 관측을 추가한다. `io`는 snapshot 켬에서 C의 실제 저장 호출 구간·스레드 CPU와 200ms 간격 프로세스 I/O 누계를 추가한다.

```sh
python3 tools/diagnostics/experiment.py run --suite trace --output var/diagnostics/trace-smoke
python3 tools/diagnostics/experiment.py run --suite io --output var/diagnostics/io-smoke
python3 tools/diagnostics/experiment.py analyze --bundle var/diagnostics/trace-smoke --output var/diagnostics/trace-smoke-analysis.json
python3 tools/diagnostics/experiment.py analyze --bundle var/diagnostics/io-smoke --output var/diagnostics/io-smoke-analysis.json
```

smoke는 실행 경로를 확인한다. 6초 동안 실제 snapshot 게시가 없을 수도 있으며 성능·장시간 안전성의 근거로 쓰지 않는다.

## 기존 10분 조건 실행하기

긴 실행은 `--preset ten-minute`를 명시한다. 거래 설정은 [config-v1.json](../config/benchmarks/config-v1.json)과 [soak-10m-v1.json](../config/benchmarks/soak-10m-v1.json)을 복사한다. 100 requests/s, warmup 5초 뒤 측정 600초, snapshot 켬은 5초 주기, 서버/생성기 계측은 500ms 간격이다.

| suite | 기본 조건·순서 | 기본 반복 | 순수 측정 시간 |
| --- | --- | --- | --- |
| `soak` | 끔→켬, 켬→끔, 끔→켬 | 3쌍 | 60분 |
| `trace` | snapshot 끔→켬, 두 프로세스 trace | 1쌍 | 20분 |
| `io` | snapshot 켬, trace·C 호출·OS 누계 | 1회 | 10분 |
| `timer` | timer만 trace 끔→켬, 2,048 worker trace 켬→끔 | 네 조건 1세트 | 40분 |

타이머는 100회/s, 관찰/worker 지연 50ms 한도, 10ms 초과 trace marker를 사용한다. 초기 epoch는 20ms 뒤다. 거래 큐·HTTP·저장 호출은 포함하지 않는다. `--repeats 1`~`3`은 조건 집합의 반복 수를 바꾼다. 조건 사이 기본 cooldown은 30초이며 `--cooldown 0`~`600`으로 바꿀 수 있다. 실제 시간에는 warmup·복구·빌드·분석·cooldown이 더해진다.

```sh
python3 tools/diagnostics/experiment.py run --suite soak --preset ten-minute --output var/diagnostics/soak-10m
python3 tools/diagnostics/experiment.py run --suite trace --preset ten-minute --output var/diagnostics/trace-10m
python3 tools/diagnostics/experiment.py run --suite io --preset ten-minute --output var/diagnostics/io-10m
python3 tools/diagnostics/experiment.py run --suite timer --preset ten-minute --output var/diagnostics/timer-10m
```

원하는 suite를 하나씩 실행한다. 동시 실행은 호스트 경쟁을 바꾸므로 비교 실험에는 사용하지 않는다. 분석은 완료 후 위 smoke와 동일한 `analyze --bundle ... --output ...` 형식이다. 이번 도구 편입 검증에서는 이 장시간 실험을 다시 실행하지 않았다.

## 보존과 실패 판정

출력 묶음은 계속 Git 제외 `var/`에 둔다. 도구·fixture·템플릿·명령만 저장소가 관리한다.

보관 용량과 기존 자료의 정리 후보는 [실험 자료 미리보기](artifact-retention.md)로 확인한다. 원본/고정 빌드·참조를 보호하고 JSON/Markdown 목록만 생성하며 실제 삭제는 수행하지 않는다.

| 파일 | 의미 |
| --- | --- |
| `protocol.json`, `environment.json` | 조건 순서·반복·기준 revision/dirty·source/binary hash·환경 |
| `bin/`, `overlay/`, `overlay.json` | 이 실행에서 빌드한 바이너리와 적용한 계측 사본 |
| 거래 case의 `loadgen/`, `run/`, stdout/stderr | 입력·실제 argv·완료/실패·dispatch/자원·Genesis/WAL/snapshot |
| `verification.json`, 재시작 전후 JSONL | 전체 최초 결과·주문·잔고/예약·공급량·호가·체결·중복 재시도·WAL 불변 검사 |
| 타이머 case의 `run.json`, `samples.jsonl`, 선택 `timer.trace` | 단조 시각의 각 경계·실패·선택적 trace |
| `manifest.json`, `completed.json` | 전체 파일 목록/해시와 그 목록의 SHA-256 완료 표시 |
| `failed.json` | 실패 사유와 완료한 case. 성공 완료 표시를 만들지 않음 |

`verify`는 파일/목록/완료 표시와 archive member 해시를 검사한다. `analyze`는 먼저 이를 검사한 뒤 현재 source로 재집계기를 빌드한다. 보관된 바이너리는 실행하지 않는다. 거래 표본을 Go `bench.Build/Summarize/SummarizeWindows`와 Python으로 재계산하고, timer 실패·trace marker·C/OS 관측을 대조한다. hash를 다시 계산한 의미 변조도 검사 대상이다. 출력 JSON은 입력 묶음 밖의 새 파일이어야 한다. 성공 보고서를 입력에 추가해 원본을 바꾸지 않는다.

실행 완료 `suite.status=PASSED`는 자료 수집·복구 검증의 완료다. `GENERATOR_LATE` 등은 그대로 기록하고 `measurementValid=false`와 구간별 지연을 유지한다. 분석은 진단 실행을 처리량 baseline으로 채택하지 않는다. Go 재집계 원본에는 다른 진단 간격도 허용하지만 이 suite의 고정 수집 간격은 500ms다.

중단된 새 묶음의 파일 보존 여부만 확인하려면 다음을 사용한다. 결과는 `completed=false`이며 `analyze`는 불완전한 묶음을 거절한다.

```sh
python3 tools/diagnostics/experiment.py verify --bundle var/diagnostics/interrupted --allow-failed --output var/diagnostics/interrupted-integrity.json
```

상태 JSONL과 snapshot을 압축하려면 거래 `run`에 `--archive`를 추가한다. 기본값은 원본과 압축본 모두 보존이다. 디스크를 줄이려면 `--archive --prune-archived`를 함께 지정한다. **이번 실행에서 새로 생성한** 대상 파일의 압축 member·크기·해시를 모두 확인한 뒤에만 해당 원본을 삭제한다. Genesis/WAL·부하 원본·검증 결과는 남기며 `verify/analyze`는 archive를 추출하지 않고 검사한다. 기존 묶음을 삭제하는 별도 명령은 제공하지 않는다.

## 과거 묶음 재분석과 계측 유지

최근 네 진단 보고서의 `artifact-manifest.json` 형식도 지원한다. 기존 묶음 경로를 `--bundle`에 지정하고 새 보고서는 밖에 둔다. 경로·case 이름·관찰 event 시각·600초를 분석 코드에 고정하지 않았다. 이전 binary는 사용하지 않으며 원본 파일 hash를 다시 확인한다. 이전 형식은 완료 표시 대신 보존 당시의 inventory를 검증하므로 결과에 `legacyInventory=true`로 구분한다.

```sh
python3 tools/diagnostics/experiment.py analyze --bundle var/timer-trace-control-20261106.vfehlj86 --output var/diagnostics/historical-timer-analysis.json
```

이 예시의 과거 묶음은 로컬 보존 자료이며 새 checkout에 포함되지 않는다. 새 실행에는 필요 없다. 이번 편입에서 최근 네 묶음을 실제 재분석했고, 당시 보고서와 수집 자료는 변경하지 않았다.

[assets](../tools/diagnostics/assets/sources.json)의 source overlay는 프로덕션 파일을 수정하지 않고 `go build -overlay`로 trace·C 호출 계측을 추가한다. [sources.json](../tools/diagnostics/assets/sources.json)의 원본 SHA가 다르면 빌드를 거절한다. 해당 프로덕션 코드를 변경할 때 계측 템플릿도 검토·갱신하고 smoke를 다시 실행해야 한다. 해시만 바꿔 검사를 우회하지 않는다.

분석 JSON에는 분석 Python source 해시도 남긴다. 구간별 CPU·GC·저장 히스토그램, 미전송 전후 bracket, Go runnable/timer/select/syscall 상태, 긴 WAL 호출의 OS 누계 bracket을 제공한다. UTC 연결은 근사이며 clock regression·관측 경계 누락은 unavailable로 표시한다. 호스트 `iostat`는 헤더에 따라 0개 이상 디스크를 처리한다. CPU만 표시되면 `diskAvailable=false`, 디스크 수치는 null이며, 여러 디스크는 표시된 장치의 합계다. 논리 장치가 겹칠 수 있어 물리 장치 전체 처리량으로 해석하지 않는다. 프로세스 I/O는 호출 내부 커널 원인이나 요청별 디스크 비용을 증명하지 않는다. trace 계측 자체의 영향과 호스트 상태도 별도 고려한다. 실제 전원 장애 검증과 관리자 커널 추적은 여전히 미완료다.

## 호스트 관찰과 현재 기준 반복

`baseline-build`로 clean 생산 바이너리를 고정하고 `baseline --preset smoke|reference`로 유휴 gate·전원·자원 예산·교차 순서를 적용한다. [명령과 판정 경계](controlled-baseline.md)를 따른다.

## 기준·후보 비교 보고서

`compare --spec ... --output 새디렉터리`는 보관 원본을 재검증해 JSON·Markdown을 함께 생성한다. P6 판정은 기존 benchreport를 재사용하고 진단·무효·반복 부족·조건 차이를 보고서에 보존한다. [비교 명세·실행·판정 안내](experiment-comparison.md)를 따른다. 보고서 생성 성공과 성능 검토 가능 상태는 별도다.

## 스냅샷 창과 응답 tail 대조

P6 snapshot 원본의 창별 요청과 서버 누적 계측을 대조하려면 다음 명령을 사용한다. 독립된 새 출력만 허용하며 기존 benchreport로 먼저 원본을 재검증한다. JSON과 Markdown, 원본 hash·창/응답 tail·단계 차분·coarse 호스트 UTC를 남긴다.

```sh
python3 tools/diagnostics/snapshot_windows.py --output var/snapshot-window-analysis var/lower-rate-reference/snapshot-1 var/lower-rate-resumed-3/snapshot-2 var/lower-rate-resumed-3/snapshot-3
```

이 경로는 로컬 보존 자료 예시다. 각 5초 cohort의 마지막 응답이 창 끝을 넘으면 그 응답 이후 수집을 시작한 다음 서버 관측까지 확장한다. 수집 도중 응답이 끝났다면 수집 종료 시각만으로는 모든 카운터의 반영을 보장하지 못하므로 다음 관측을 사용한다. 따라서 인접 창의 서버 관측이 겹치거나 다른 작업을 포함할 수 있다. 개별 요청의 인과 관계·물리 I/O 원인이나 성능 채택을 자동 판정하지 않는다. GC pause와 병렬 GC CPU, snapshot 호출과 상태 복사, 누적 최대와 구간 최대를 구분한다.

2026-11-09 머지 검토에서 수집 중 끝난 응답의 카운터 누락 가능성을 보정했다. 수집 종료 시각뿐 아니라 마지막 응답 이후 수집 시작 시각을 요구한다. 경계 회귀 2개를 보완했고 보관 snapshot 3개를 재분석해 기존 관측/분포/판정이 동일함을 확인했다. 당시 원본과 보고서는 이전 source hash로 보존한다.

## 자동 검증

```sh
python3 -m unittest discover -s tools/diagnostics -p 'test_*.py'
python3 -O -m unittest discover -s tools/diagnostics -p 'test_*.py'
scripts/go test -race ./tools/timerprobe ./tools/experimentcheck
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

공용 CI 스크립트는 Go 검사 뒤 Python 회귀 검사와 새 묶음 smoke를 실행한다. portable은 타이머 네 조건, native는 trace·C/OS I/O·거래/복구·archive를 검사한다. 두 smoke 모두 기존 출력 재사용과 재해시한 의미 변조가 거절되는지도 확인한다. CI의 smoke 묶음은 임시 디렉터리이며 검사 후 정리한다. 수동 `run`은 완료·실패 원본을 보존한다. 전체 CI는 지원 플랫폼/권한에서 실행하며 실제 GitHub CI 통과 여부는 push 후 별도로 확인한다.
